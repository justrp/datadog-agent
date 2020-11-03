// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-2020 Datadog, Inc.

// +build linux_bpf

package probe

import (
	"time"

	"github.com/DataDog/datadog-agent/pkg/security/ebpf"
	"github.com/DataDog/datadog-agent/pkg/security/rules"
	"github.com/DataDog/datadog-agent/pkg/security/secl/eval"
)

type activeDiscarder = activeKFilter
type activeDiscarders = activeKFilters

type pidDiscarder struct {
	eventType EventType
	pid       uint32
	padding   uint32
}

type pidDiscarderParameters struct {
	timestamp uint64
}

func discardPID(probe *Probe, eventType EventType, pid uint32) (activeDiscarder, error) {
	key := pidDiscarder{
		eventType: eventType,
		pid:       pid,
	}

	return &mapEntry{
		tableName: "pid_discarders",
		key:       key,
		tableKey:  &key,
		value:     &pidDiscarderParameters{},
	}, nil
}

func discardPIDWithTimeout(probe *Probe, eventType EventType, pid uint32, timeout time.Duration) (activeDiscarder, error) {
	key := pidDiscarder{
		eventType: eventType,
		pid:       pid,
	}
	params := pidDiscarderParameters{
		timestamp: uint64(probe.resolvers.TimeResolver.ComputeMonotonicTimestamp(time.Now().Add(timeout))),
	}

	return &mapEntry{
		tableName: "pid_discarders",
		key:       key,
		tableKey:  &key,
		value:     &params,
	}, nil
}

type inodeDiscarder struct {
	eventType EventType
	pathKey   PathKey
}

func removeDiscarderInode(probe *Probe, mountID uint32, inode uint64) {
	key := inodeDiscarder{
		pathKey: PathKey{
			MountID: mountID,
			Inode:   inode,
		},
	}

	table := probe.Map("inode_discarders")
	for eventType := UnknownEventType + 1; eventType != maxEventType; eventType++ {
		key.eventType = eventType
		table.Delete(&key)
	}
}

func discardInode(probe *Probe, eventType EventType, mountID uint32, inode uint64) (activeDiscarder, error) {
	key := inodeDiscarder{
		eventType: eventType,
		pathKey: PathKey{
			MountID: mountID,
			Inode:   inode,
		},
	}

	return &mapEntry{
		tableName: "inode_discarders",
		key:       key,
		tableKey:  &key,
		value:     ebpf.ZeroUint8MapItem,
	}, nil
}

func discardParentInode(probe *Probe, rs *rules.RuleSet, eventType EventType, field eval.Field, filename string, mountID uint32, inode uint64, pathID uint32) (activeDiscarder, error) {
	isDiscarder, err := isParentPathDiscarder(rs, eventType, field, filename)
	if !isDiscarder {
		return nil, err
	}

	parentMountID, parentInode, err := probe.resolvers.DentryResolver.GetParent(mountID, inode, pathID)
	if err != nil {
		return nil, err
	}

	return discardInode(probe, eventType, parentMountID, parentInode)
}

func discardFlags(probe *Probe, tableName string, flags ...int) (activeDiscarder, error) {
	return setFlagsFilter(probe, tableName, flags...)
}
