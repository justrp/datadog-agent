// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-2020 Datadog, Inc.

// +build linux_bpf

package probe

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	lib "github.com/DataDog/ebpf"
	"github.com/DataDog/ebpf/manager"
	"github.com/pkg/errors"

	"github.com/DataDog/datadog-agent/pkg/security/ebpf"
	"github.com/DataDog/datadog-agent/pkg/security/ebpf/probes"
	"github.com/DataDog/datadog-agent/pkg/security/utils"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/gopsutil/process"
)

var snapshotProbeIDs = []manager.ProbeIdentificationPair{
	{
		UID:     probes.SecurityAgentUID,
		Section: "kretprobe/get_task_exe_file",
	},
}

// InodeInfo holds information related to inode from kernel
type InodeInfo struct {
	MountID         uint32
	OverlayNumLower int32
}

// UnmarshalBinary unmarshals a binary representation of itself
func (i *InodeInfo) UnmarshalBinary(data []byte) (int, error) {
	if len(data) < 8 {
		return 0, ErrNotEnoughData
	}
	i.MountID = ebpf.ByteOrder.Uint32(data)
	i.OverlayNumLower = int32(ebpf.ByteOrder.Uint32(data[4:]))
	return 8, nil
}

// ProcessResolver resolved process context
type ProcessResolver struct {
	sync.RWMutex
	probe          *Probe
	resolvers      *Resolvers
	snapshotProbes []*manager.Probe
	inodeInfoMap   *lib.Map
	procCacheMap   *lib.Map
	pidCookieMap   *lib.Map

	entryCache map[uint32]*ProcessCacheEntry
}

// GetProbes returns the probes required by the snapshot
func (p *ProcessResolver) GetProbes() []*manager.Probe {
	return p.snapshotProbes
}

// AddEntry add an entry to the local cache
func (p *ProcessResolver) AddEntry(pid uint32, entry *ProcessCacheEntry) {
	p.insertEntry(pid, entry)
}

// DumpCache prints the process cache to the console
func (p *ProcessResolver) DumpCache() {
	fmt.Println("Dumping process cache ...")
	for _, entry := range p.entryCache {
		fmt.Printf("%s\n", entry)
	}
}

// enrichEventFromProc uses /proc to enrich a ProcessCacheEntry with additional metadata
func (p *ProcessResolver) enrichEventFromProc(entry *ProcessCacheEntry, proc *process.FilledProcess) error {
	pid := uint32(proc.Pid)

	// Get process filename and pre-fill the cache
	procExecPath := utils.ProcExePath(pid)
	pathnameStr, err := os.Readlink(procExecPath)
	if err != nil {
		log.Debug(errors.Wrapf(err, "snapshot failed for %d: couldn't readlink binary", pid))
		return err
	}
	if pathnameStr == "/ (deleted)" {
		log.Debugf("snapshot failed for %d: binary was deleted", pid)
		return errors.New("snapshot failed")
	}

	// Get the inode of the process binary
	fi, err := os.Stat(procExecPath)
	if err != nil {
		log.Debug(errors.Wrapf(err, "snapshot failed for %d: couldn't stat binary", pid))
		return err
	}
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		log.Debugf("snapshot failed for %d: couldn't stat binary", pid)
		return errors.New("snapshot failed")
	}
	inode := stat.Ino

	info, err := p.retrieveInodeInfo(inode)
	if err != nil {
		log.Debug(errors.Wrapf(err, "snapshot failed for %d: couldn't retrieve inode info", pid))
		return err
	}

	// Retrieve the container ID of the process from /proc
	containerID, err := p.resolvers.ContainerResolver.GetContainerID(pid)
	if err != nil {
		log.Debug(errors.Wrapf(err, "snapshot failed for %d: couldn't parse container ID", pid))
		return err
	}

	entry.FileEvent = FileEvent{
		Inode:           inode,
		OverlayNumLower: info.OverlayNumLower,
		MountID:         info.MountID,
		PathnameStr:     pathnameStr,
	}
	// resolve container path with the MountResolver
	entry.FileEvent.ResolveContainerPathWithResolvers(p.resolvers)

	entry.ContainerContext.ID = string(containerID)
	entry.ExecTimestamp = time.Unix(0, proc.CreateTime*int64(time.Millisecond))
	entry.Comm = proc.Name
	entry.PPid = uint32(proc.Ppid)
	entry.TTYName = utils.PidTTY(pid)
	entry.ProcessContext.Pid = pid
	entry.ProcessContext.Tid = pid
	if len(proc.Uids) > 0 {
		entry.ProcessContext.UID = uint32(proc.Uids[0])
	}
	if len(proc.Gids) > 0 {
		entry.ProcessContext.GID = uint32(proc.Gids[0])
	}
	return nil
}

// retrieveInodeInfo fetches inode metadata from kernel space
func (p *ProcessResolver) retrieveInodeInfo(inode uint64) (*InodeInfo, error) {
	inodeb := make([]byte, 8)

	ebpf.ByteOrder.PutUint64(inodeb, inode)
	data, err := p.inodeInfoMap.LookupBytes(inodeb)
	if err != nil {
		return nil, err
	}

	if data == nil {
		return nil, errors.New("not found")
	}

	var info InodeInfo
	if _, err := info.UnmarshalBinary(data); err != nil {
		return nil, err
	}

	return &info, nil
}

// insertEntry inserts an event in the cache and ensures that the lineage of the new entry is properly updated
func (p *ProcessResolver) insertEntry(pid uint32, entry *ProcessCacheEntry) {
	p.Lock()

	// check for an existing entry first to update processes lineage
	parent, ok := p.entryCache[entry.PPid]
	if ok {
		parent.Children = append(parent.Children, entry)
	} else {
		if entry.PPid >= 1 {
			// create an entry for the parent, if the parent exists it might be populated later
			parent = &ProcessCacheEntry{
				ProcessContext: ProcessContext{
					Pid: entry.PPid,
				},
				Children: []*ProcessCacheEntry{entry},
			}
			p.entryCache[entry.PPid] = parent
		}
	}
	entry.Parent = parent
	p.entryCache[pid] = entry

	p.Unlock()
}

func (p *ProcessResolver) DelEntry(pid uint32) {
	p.Lock()
	defer p.Unlock()
	delete(p.entryCache, pid)
}

// Resolve returns the cache entry for the given pid
func (p *ProcessResolver) Resolve(pid uint32) *ProcessCacheEntry {
	p.Lock()
	defer p.Unlock()
	entry, exists := p.entryCache[pid]
	if exists {
		return entry
	}

	// fallback request the map directly, the perf event may be delayed
	return p.resolve(pid)
}

func (p *ProcessResolver) resolve(pid uint32) *ProcessCacheEntry {
	pidb := make([]byte, 4)
	ebpf.ByteOrder.PutUint32(pidb, pid)

	cookieb, err := p.pidCookieMap.LookupBytes(pidb)
	if err != nil || cookieb == nil {
		return nil
	}

	// first 4 bytes are the actual cookie
	entryb, err := p.procCacheMap.LookupBytes(cookieb[0:4])
	if err != nil || entryb == nil {
		return nil
	}

	var entry ProcessCacheEntry
	data := append(entryb, cookieb...)
	if len(data) < 208 {
		// not enough data
		return nil
	}
	read, err := entry.UnmarshalBinary(data, p.resolvers, true)
	if err != nil {
		return nil
	}

	entry.UID = ebpf.ByteOrder.Uint32(data[read:read+4])
	entry.GID = ebpf.ByteOrder.Uint32(data[read+4:read+8])
	entry.Pid = pid
	entry.Tid = pid

	p.insertEntry(pid, &entry)

	return &entry
}

func (p *ProcessResolver) Get(pid uint32) *ProcessCacheEntry {
	p.RLock()
	defer p.RUnlock()
	entry, exists := p.entryCache[pid]
	if exists {
		return entry
	}
	return nil
}

// Start starts the resolver
func (p *ProcessResolver) Start() error {
	// initializes the list of snapshot probes
	for _, id := range snapshotProbeIDs {
		probe, ok := p.probe.manager.GetProbe(id)
		if !ok {
			return errors.Errorf("couldn't find probe %s", id)
		}
		p.snapshotProbes = append(p.snapshotProbes, probe)
	}

	p.inodeInfoMap = p.probe.Map("inode_info_cache")
	if p.inodeInfoMap == nil {
		return errors.New("map inode_info_cache not found")
	}
	p.procCacheMap = p.probe.Map("proc_cache")
	if p.procCacheMap == nil {
		return errors.New("map proc_cache not found")
	}
	p.pidCookieMap = p.probe.Map("pid_cache")
	if p.pidCookieMap == nil {
		return errors.New("map pid_cache not found")
	}

	return nil
}

// SyncCache snapshots /proc for the provided pid. This method returns true if it updated the process cache.
func (p *ProcessResolver) SyncCache(proc *process.FilledProcess) bool {
	pid := uint32(proc.Pid)
	if pid == 0 {
		return false
	}

	// Only a R lock is necessary to check if the entry exists, but if it exists, we'll update it, so a RW lock is
	// required.
	p.Lock()

	// Check if an entry is already in cache for the given pid.
	entry, inCache := p.entryCache[pid]
	if inCache && !entry.ExecTimestamp.IsZero() {
		p.Unlock()
		return false
	}
	if !inCache {
		entry = &ProcessCacheEntry{}
	}

	// update the cache entry
	if err := p.enrichEventFromProc(entry, proc); err != nil {
		p.Unlock()
		return false
	}

	// If entry is a new entry, the lock was not required, only the lock in `insertEntry` matters.
	// If entry is already in cache, the lock was necessary until we are done updating it (i.e. after enrichEvent()).
	// In both cases, we can unlock the process cache.
	p.Unlock()
	p.insertEntry(pid, entry)

	log.Tracef("New process cache entry added: %s %s %d/%d", proc.Name, entry.PathnameStr, pid, entry.Inode)

	return true
}

// NewProcessResolver returns a new process resolver
func NewProcessResolver(probe *Probe, resolvers *Resolvers) (*ProcessResolver, error) {
	return &ProcessResolver{
		probe:      probe,
		resolvers:  resolvers,
		entryCache: make(map[uint32]*ProcessCacheEntry),
	}, nil
}
