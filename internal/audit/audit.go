// Package audit writes audit-log rows for tock's mutating operations.
//
// The table, its morph aliases and the recorded events are upstream's (the
// `owen-it/laravel-auditing` package wired through App\Models\Concerns\
// CustomAuditable). What upstream leaves to Eloquent observers, tock does
// explicitly in the handlers — the queries are generated, so there is no ORM
// magic to hang an observer on.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
)

// Events recorded by upstream (config/audit.php `events`).
const (
	EventCreated = "created"
	EventUpdated = "updated"
	EventDeleted = "deleted"
)

// Aliases are the enforced morph-map names; `auditable_type` stores these,
// not class names.
const (
	TypeClient                 = "client"
	TypeMember                 = "membership"
	TypeOrganization           = "organization"
	TypeOrganizationInvitation = "organization-invitation"
	TypeProject                = "project"
	TypeProjectMember          = "project-member"
	TypeTag                    = "tag"
	TypeTask                   = "task"
	TypeTimeEntry              = "time-entry"
	TypeUser                   = "user"
	// tock-owned (phase 7/8): invoicing has no upstream morph alias.
	TypeInvoice          = "invoice"
	TypeInvoiceRecipient = "invoice-recipient"
)

// ActorTypeUser is the morph alias of the actor when a user made the change.
const ActorTypeUser = "user"

// Meta is the request provenance stored on every row.
type Meta struct {
	ActorID   pgtype.UUID
	URL       string
	IP        string // already anonymized
	UserAgent string
}

// Entry is one audit row.
type Entry struct {
	Meta        Meta
	Event       string
	Type        string
	AuditableID pgtype.UUID
	OwnerOrgID  pgtype.UUID
	OwnerUserID pgtype.UUID
	OldValues   map[string]any
	NewValues   map[string]any
}

// Diff returns the changed subset of two attribute snapshots, the way the
// auditing package records an `updated` event: only dirty attributes, old and
// new.
func Diff(before, after map[string]any) (map[string]any, map[string]any) {
	old := map[string]any{}
	new := map[string]any{}
	for key, afterValue := range after {
		beforeValue, existed := before[key]
		if existed && reflect.DeepEqual(beforeValue, afterValue) {
			continue
		}
		if existed {
			old[key] = beforeValue
		}
		new[key] = afterValue
	}
	return old, new
}

// Record inserts the row. Both empty value sets are skipped: upstream runs with
// `empty_values => false`, so an update that only touched excluded attributes
// leaves no trace.
func Record(ctx context.Context, q *db.Queries, e Entry) error {
	if len(e.OldValues) == 0 && len(e.NewValues) == 0 {
		return nil
	}
	oldValues, err := encodeValues(e.OldValues)
	if err != nil {
		return err
	}
	newValues, err := encodeValues(e.NewValues)
	if err != nil {
		return err
	}

	var actorType *string
	if e.Meta.ActorID.Valid {
		actorType = ptr(ActorTypeUser)
	}
	var url, userAgent *string
	if e.Meta.URL != "" {
		url = ptr(e.Meta.URL)
	}
	if e.Meta.UserAgent != "" {
		ua := e.Meta.UserAgent
		if len(ua) > 1023 {
			ua = ua[:1023]
		}
		userAgent = &ua
	}
	var ip *netip.Addr
	if e.Meta.IP != "" {
		if parsed, parseErr := netip.ParseAddr(e.Meta.IP); parseErr == nil {
			ip = &parsed
		}
	}

	_, err = q.InsertAudit(ctx, db.InsertAuditParams{
		ActorType:           actorType,
		ActorID:             e.Meta.ActorID,
		Event:               e.Event,
		AuditableType:       e.Type,
		AuditableID:         e.AuditableID,
		OldValues:           oldValues,
		NewValues:           newValues,
		Url:                 url,
		IpAddress:           ip,
		UserAgent:           userAgent,
		OwnerUserID:         e.OwnerUserID,
		OwnerOrganizationID: e.OwnerOrgID,
	})
	if err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	return nil
}

// encodeValues renders an attribute map the way upstream's json column does:
// an empty set is `[]` (PHP's empty array), otherwise an object.
func encodeValues(values map[string]any) ([]byte, error) {
	if len(values) == 0 {
		return []byte("[]"), nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode audit values: %w", err)
	}
	return encoded, nil
}

// AnonymizeIP zeroes the host part the way App\Extensions\Auditing\Resolvers\
// CustomIpAddressResolver does: the last IPv4 octet becomes 0, the last two
// IPv6 hextets become 0:0. Unparsable input yields "" (stored as NULL).
func AnonymizeIP(raw string) string {
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return ""
	}
	if addr.Is4() || addr.Is4In6() {
		bytes := addr.As4()
		bytes[3] = 0
		return netip.AddrFrom4(bytes).String()
	}
	bytes := addr.As16()
	bytes[12], bytes[13], bytes[14], bytes[15] = 0, 0, 0, 0
	return netip.AddrFrom16(bytes).String()
}

func ptr[T any](v T) *T { return &v }
