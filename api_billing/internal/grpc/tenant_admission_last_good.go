package grpc

import (
	"context"
	"sync"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/protobuf/proto"
)

// tenantAdmissionLastGoodTTL bounds how long an admission decision read from
// the database stays servable while the database cannot answer. A slow or
// failing read is not a billing change, so admission keeps the tenant's last
// read state instead of flipping it to deny; past this age the decision is no
// longer authority and the RPC fails closed.
const tenantAdmissionLastGoodTTL = 5 * time.Minute

// tenantAdmissionReplyReserve is the part of the caller's remaining deadline
// kept for answering after the database read gives up, so a last-good
// decision still reaches a caller whose deadline is shorter than the query
// budget.
const tenantAdmissionReplyReserve = 50 * time.Millisecond

// tenantAdmissionLastGood holds, per tenant, the most recent admission
// decision read from the database. Entries exist only for tenants with a
// subscription row, so the map is bounded by the subscription count.
type tenantAdmissionLastGood struct {
	mu      sync.Mutex
	entries map[string]tenantAdmissionLastGoodEntry
	now     func() time.Time
}

type tenantAdmissionLastGoodEntry struct {
	response *purserpb.GetTenantAdmissionStatusResponse
	readAt   time.Time
}

func (c *tenantAdmissionLastGood) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *tenantAdmissionLastGood) store(tenantID string, response *purserpb.GetTenantAdmissionStatusResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]tenantAdmissionLastGoodEntry)
	}
	c.entries[tenantID] = tenantAdmissionLastGoodEntry{
		response: cloneTenantAdmissionStatus(response),
		readAt:   c.clock(),
	}
}

// forget drops a tenant whose subscription the database reported missing, so
// a later fault cannot resurrect an admission the tenant no longer holds.
func (c *tenantAdmissionLastGood) forget(tenantID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, tenantID)
}

// load returns a copy of the tenant's last decision and its age when it is
// younger than tenantAdmissionLastGoodTTL.
func (c *tenantAdmissionLastGood) load(tenantID string) (*purserpb.GetTenantAdmissionStatusResponse, time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[tenantID]
	if !ok {
		return nil, 0, false
	}
	age := c.clock().Sub(entry.readAt)
	if age > tenantAdmissionLastGoodTTL {
		delete(c.entries, tenantID)
		return nil, 0, false
	}
	clone := cloneTenantAdmissionStatus(entry.response)
	return clone, age, clone != nil
}

func cloneTenantAdmissionStatus(response *purserpb.GetTenantAdmissionStatusResponse) *purserpb.GetTenantAdmissionStatusResponse {
	clone, ok := proto.Clone(response).(*purserpb.GetTenantAdmissionStatusResponse)
	if !ok {
		return nil
	}
	return clone
}

// tenantAdmissionQueryBudget is tenantAdmissionQueryTimeout, shortened when
// the caller's deadline would expire first, minus the reply reserve.
func tenantAdmissionQueryBudget(ctx context.Context) time.Duration {
	budget := tenantAdmissionQueryTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline) - tenantAdmissionReplyReserve; remaining < budget {
			budget = remaining
		}
	}
	return budget
}
