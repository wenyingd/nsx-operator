package childsubnet

import (
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
	"github.com/vmware/vsphere-automation-sdk-go/runtime/data"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
)

type (
	SegmentConnectionBindingMap model.SegmentConnectionBindingMap
	Segment                     model.Segment
)

func (scbm *SegmentConnectionBindingMap) Key() string {
	return *scbm.Id
}

func (scbm *SegmentConnectionBindingMap) Value() data.DataValue {
	s := &SegmentConnectionBindingMap{Id: scbm.Id, DisplayName: scbm.DisplayName, Tags: scbm.Tags}
	dataValue, _ := ComparableToSegmentConnectionBindingMap(s).GetDataValue__()
	return dataValue
}

func (s *Segment) Key() string {
	return *s.Id
}

func (s *Segment) Value() data.DataValue {
	segment := &Segment{Id: s.Id, DisplayName: s.DisplayName, Tags: s.Tags}
	dataValue, _ := ComparableToSegment(segment).GetDataValue__()
	return dataValue
}

func SegmentConnectionBindingMapToComparable(scbm *model.SegmentConnectionBindingMap) common.Comparable {
	return (*SegmentConnectionBindingMap)(scbm)
}

func SegmentConnectionBindingMapsToComparable(scbms []*model.SegmentConnectionBindingMap) []common.Comparable {
	bindingMaps := make([]common.Comparable, len(scbms))
	for i := range scbms {
		bindingMap := SegmentConnectionBindingMapToComparable(scbms[i])
		bindingMaps = append(bindingMaps, bindingMap)
	}
	return bindingMaps
}

func ComparableToSegmentConnectionBindingMap(scbm common.Comparable) *model.SegmentConnectionBindingMap {
	return (*model.SegmentConnectionBindingMap)(scbm.(*SegmentConnectionBindingMap))
}

func ComparableToSegmentConnectionBindingMaps(scbms []common.Comparable) []*model.SegmentConnectionBindingMap {
	bindingMaps := make([]*model.SegmentConnectionBindingMap, len(scbms))
	for i := range scbms {
		bindingMap := ComparableToSegmentConnectionBindingMap(scbms[i])
		bindingMaps = append(bindingMaps, bindingMap)
	}
	return bindingMaps
}

func SegmentToComparable(s *model.Segment) common.Comparable {
	return (*Segment)(s)
}

func ComparableToSegment(s common.Comparable) *model.Segment {
	return (*model.Segment)(s.(*Segment))
}

func SegmentsToComparable(ss []*model.Segment) []common.Comparable {
	segments := make([]common.Comparable, len(ss))
	for i := range ss {
		segment := SegmentToComparable(ss[i])
		segments = append(segments, segment)
	}
	return segments
}

func ComparableToSegments(ss []common.Comparable) []*model.Segment {
	segments := make([]*model.Segment, len(ss))
	for i := range ss {
		segment := ComparableToSegment(ss[i])
		segments = append(segments, segment)
	}
	return segments
}
