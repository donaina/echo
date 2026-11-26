package transfer

import (
	"context"
	"sync"
)

// StubDestination is the test double this stage requires: it can apply,
// reject, or return unknown, scripted per reference. S7 and S8 replace it with
// the core and the rail, and it is deleted then.
type StubDestination struct {
	mu        sync.Mutex
	credits   map[string][]CreditStatus
	enquiries map[string]CreditStatus
	applied   map[string]int
	asked     map[string]int
}

// NewStubDestination returns a destination that applies every credit.
func NewStubDestination() *StubDestination {
	return &StubDestination{
		credits:   map[string][]CreditStatus{},
		enquiries: map[string]CreditStatus{},
		applied:   map[string]int{},
		asked:     map[string]int{},
	}
}

// Script queues the outcomes Credit returns for reference, one per call.
// Once the queue is empty, Credit applies.
func (s *StubDestination) Script(reference string, outcomes ...CreditStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credits[reference] = append(s.credits[reference], outcomes...)
}

// OnEnquiry sets what Enquiry reports for reference. The default is Unknown.
func (s *StubDestination) OnEnquiry(reference string, status CreditStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enquiries[reference] = status
}

// Credit records and applies a credit, or returns the next scripted outcome.
func (s *StubDestination) Credit(_ context.Context, req CreditRequest) (CreditResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := Applied
	if queue := s.credits[req.Reference]; len(queue) > 0 {
		status = queue[0]
		s.credits[req.Reference] = queue[1:]
	}
	if status == Applied {
		s.applied[req.Reference]++
	}
	return CreditResult{Status: status, RemoteRef: "remote-" + req.Reference}, nil
}

// Enquiry reports the scripted status, or Unknown when nothing was scripted.
func (s *StubDestination) Enquiry(_ context.Context, reference string) (CreditResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked[reference]++
	status, ok := s.enquiries[reference]
	if !ok {
		status = Unknown
	}
	return CreditResult{Status: status, RemoteRef: "remote-" + reference}, nil
}

// AppliedCount is how many credits the destination actually applied for
// reference. It is what "exactly once" is measured against.
func (s *StubDestination) AppliedCount(reference string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applied[reference]
}

// EnquiryCount is how many times the saga asked.
func (s *StubDestination) EnquiryCount(reference string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asked[reference]
}
