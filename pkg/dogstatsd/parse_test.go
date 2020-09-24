package dogstatsd

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIdentifyEvent(t *testing.T) {
	metricSample := []byte("_e{4,5}:title|text|#shell,bash")
	messageType := findMessageType(metricSample)
	assert.Equal(t, eventType, messageType)
}

func TestIdentifyServiceCheck(t *testing.T) {
	metricSample := []byte("_sc|NAME|STATUS|d:TIMESTAMP|h:HOSTNAME|#TAG_KEY_1:TAG_VALUE_1,TAG_2|m:SERVICE_CHECK_MESSAGE")
	messageType := findMessageType(metricSample)
	assert.Equal(t, serviceCheckType, messageType)
}

func TestIdentifyMetricSample(t *testing.T) {
	metricSample := []byte("song.length:240|h|@0.5")
	messageType := findMessageType(metricSample)
	assert.Equal(t, metricSampleType, messageType)
}

func TestIdentifyRandomString(t *testing.T) {
	metricSample := []byte("song.length:240|h|@0.5")
	messageType := findMessageType(metricSample)
	assert.Equal(t, metricSampleType, messageType)
}

func TestParseTags(t *testing.T) {
	parser := newParser(newFloat64ListPool())
	rawTags := []byte("tag:test,mytag,good:boy")
	tags := parser.parseTags(rawTags)
	expectedTags := []string{"tag:test", "mytag", "good:boy"}
	assert.ElementsMatch(t, expectedTags, tags)
}

func TestParseTagsEmpty(t *testing.T) {
	parser := newParser(newFloat64ListPool())
	rawTags := []byte("")
	tags := parser.parseTags(rawTags)
	assert.Nil(t, tags)
}

func TestUnsafeParseFloat(t *testing.T) {
	rawFloat := "1.1234"

	unsafeFloat, err := parseFloat64([]byte(rawFloat))
	assert.NoError(t, err)
	float, err := strconv.ParseFloat(rawFloat, 64)
	assert.NoError(t, err)

	assert.Equal(t, unsafeFloat, float)
}

func TestUnsafeParseFloatList(t *testing.T) {
	unsafeFloats, err := parseFloat64List([]byte("1.1234:21.5:13"), []float64{})
	assert.NoError(t, err)
	assert.Len(t, unsafeFloats, 3)
	assert.Equal(t, unsafeFloats, []float64{1.1234, 21.5, 13})

	unsafeFloats, err = parseFloat64List([]byte("1.1234"), []float64{})
	assert.NoError(t, err)
	assert.Len(t, unsafeFloats, 1)
	assert.Equal(t, unsafeFloats, []float64{1.1234})

	unsafeFloats, err = parseFloat64List([]byte("1.1234:41:"), []float64{})
	assert.NoError(t, err)
	assert.Len(t, unsafeFloats, 2)
	assert.Equal(t, unsafeFloats, []float64{1.1234, 41})

	unsafeFloats, err = parseFloat64List([]byte("1.1234::41"), []float64{})
	assert.NoError(t, err)
	assert.Len(t, unsafeFloats, 2)

	assert.Equal(t, unsafeFloats, []float64{1.1234, 41})

	unsafeFloats, err = parseFloat64List([]byte(":1.1234::41"), []float64{})
	assert.NoError(t, err)
	assert.Len(t, unsafeFloats, 2)
	assert.Equal(t, unsafeFloats, []float64{1.1234, 41})

	_, err = parseFloat64List([]byte(""), []float64{})
	assert.Error(t, err)
	assert.Equal(t, err.Error(), "no value found")
}

func TestUnsafeParseInt(t *testing.T) {
	rawInt := "123"

	unsafeInteger, err := parseInt64([]byte(rawInt))
	assert.NoError(t, err)
	integer, err := strconv.ParseInt(rawInt, 10, 64)
	assert.NoError(t, err)

	assert.Equal(t, unsafeInteger, integer)
}
