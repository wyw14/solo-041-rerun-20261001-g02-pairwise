package sensor

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-97/internal/model"
)

type Sampler struct {
	mu        sync.RWMutex
	latest    map[model.LineID]map[model.SampleKind]model.Sample
	listeners []func(model.Sample)
}

func NewSampler() *Sampler {
	return &Sampler{latest: make(map[model.LineID]map[model.SampleKind]model.Sample)}
}

func (s *Sampler) Subscribe(listener func(model.Sample)) error {
	if listener == nil {
		return errors.New("sample listener is required")
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, listener)
	s.mu.Unlock()
	return nil
}

func (s *Sampler) Publish(sample model.Sample) error {
	if sample.LineID == "" || sample.Kind == "" || sample.ObservedAt.Equal(time.Time{}) {
		return errors.New("sample identity is incomplete")
	}
	s.mu.Lock()
	byKind := s.latest[sample.LineID]
	if byKind == nil {
		byKind = make(map[model.SampleKind]model.Sample)
		s.latest[sample.LineID] = byKind
	}
	previous, exists := byKind[sample.Kind]
	if exists && sample.Sequence <= previous.Sequence {
		s.mu.Unlock()
		return nil
	}
	byKind[sample.Kind] = sample
	listeners := append([]func(model.Sample){}, s.listeners...)
	s.mu.Unlock()
	for _, listener := range listeners {
		listener(sample)
	}
	return nil
}

func (s *Sampler) LineSnapshot(lineID model.LineID) []model.Sample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byKind := s.latest[lineID]
	result := make([]model.Sample, 0, len(byKind))
	for _, sample := range byKind {
		result = append(result, sample)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Kind < result[j].Kind })
	return result
}

// Latest 返回指定线指定类型的最近一条样本。
func (s *Sampler) Latest(lineID model.LineID, kind model.SampleKind) (model.Sample, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byKind := s.latest[lineID]
	if byKind == nil {
		return model.Sample{}, false
	}
	sample, ok := byKind[kind]
	return sample, ok
}
