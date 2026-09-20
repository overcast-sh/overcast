package route53

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Core business logic shared by the REST-XML handlers and the typed
// (Query-protocol) operations. Everything here is codec-agnostic: inputs are
// plain values, outputs are domain structs plus an optional *protocol.AWSError.

// changeStatusInsync is the only change status Overcast reports: changes
// apply synchronously, so GetChange never observes PENDING. This is a
// documented divergence that keeps CDK/CLI waiters fast.
const changeStatusInsync = "INSYNC"

// rrTypes are the record types Route 53 accepts.
var rrTypes = map[string]struct{}{
	"SOA": {}, "A": {}, "TXT": {}, "NS": {}, "CNAME": {}, "MX": {}, "NAPTR": {},
	"PTR": {}, "SRV": {}, "SPF": {}, "AAAA": {}, "CAA": {}, "DS": {}, "TLSA": {},
	"SSHFP": {}, "SVCB": {}, "HTTPS": {},
}

// healthCheckTypes are the health check types Route 53 accepts.
var healthCheckTypes = map[string]struct{}{
	"HTTP": {}, "HTTPS": {}, "HTTP_STR_MATCH": {}, "HTTPS_STR_MATCH": {}, "TCP": {},
	"CALCULATED": {}, "CLOUDWATCH_METRIC": {}, "RECOVERY_CONTROL": {},
}

// ─── Errors ───────────────────────────────────────────────────────────────────

func errInvalidInput(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "InvalidInput", Message: msg, HTTPStatus: http.StatusBadRequest}
}

func errInvalidDomainName(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidDomainName",
		Message:    fmt.Sprintf("The domain name %s is not valid", name),
		HTTPStatus: http.StatusBadRequest,
	}
}

func errNoSuchHostedZone(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchHostedZone",
		Message:    fmt.Sprintf("No hosted zone found with ID: %s", strings.TrimPrefix(id, "/hostedzone/")),
		HTTPStatus: http.StatusNotFound,
	}
}

func errNoSuchHealthCheck(id string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NoSuchHealthCheck",
		Message:    fmt.Sprintf("A health check with id %s does not exist.", id),
		HTTPStatus: http.StatusNotFound,
	}
}

func errHostedZoneAlreadyExists(callerRef string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "HostedZoneAlreadyExists",
		Message:    fmt.Sprintf("A hosted zone has already been created with the specified caller reference: %s", callerRef),
		HTTPStatus: http.StatusConflict,
	}
}

func errHostedZoneNotEmpty() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "HostedZoneNotEmpty",
		Message:    "The specified hosted zone contains non-required resource record sets and so cannot be deleted.",
		HTTPStatus: http.StatusBadRequest,
	}
}

func errHealthCheckAlreadyExists(callerRef string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "HealthCheckAlreadyExists",
		Message:    fmt.Sprintf("A health check already exists with the specified caller reference: %s", callerRef),
		HTTPStatus: http.StatusConflict,
	}
}

func errHealthCheckVersionMismatch(id string, got, want int64) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "HealthCheckVersionMismatch",
		Message:    fmt.Sprintf("Health check %s has version %d, but the request expected version %d", id, want, got),
		HTTPStatus: http.StatusConflict,
	}
}

// errInvalidChangeBatch renders messages in AWS's bracketed aggregate form:
// "[Tried to delete resource record set ... but it was not found]".
func errInvalidChangeBatch(messages ...string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidChangeBatch",
		Message:    "[" + strings.Join(messages, ", ") + "]",
		HTTPStatus: http.StatusBadRequest,
	}
}

// errEnumViolation mirrors the XML-schema-validation error AWS returns for a
// value outside an enumeration.
func errEnumViolation(value string, allowed string) *protocol.AWSError {
	return errInvalidInput(fmt.Sprintf(
		"Invalid XML ; cvc-enumeration-valid: Value '%s' is not facet-valid with respect to enumeration '%s'. It must be a value from the enumeration.",
		value, allowed))
}

// ─── Changes ──────────────────────────────────────────────────────────────────

// changeInfo describes a submitted change batch.
type changeInfo struct {
	ID          string // path form: /change/C...
	Status      string
	SubmittedAt time.Time
}

// recordChange registers a new change and returns its ChangeInfo.
func (s *Service) recordChange(ctx context.Context) (changeInfo, *protocol.AWSError) {
	id := newChangeID()
	if err := s.putChange(ctx, id); err != nil {
		return changeInfo{}, protocol.ErrInternalError
	}
	return changeInfo{ID: "/change/" + id, Status: changeStatusInsync, SubmittedAt: s.clk.Now()}, nil
}

// getChangeCore resolves a change's status. Unknown change IDs resolve to
// INSYNC (a documented divergence: real AWS returns NoSuchChange) so that
// CDK/CLI waiters always converge, even across a store wipe.
func (s *Service) getChangeCore(ctx context.Context, changeID string) (changeInfo, *protocol.AWSError) {
	changeID = strings.TrimPrefix(changeID, "/change/")
	status, found, err := s.getChangeStatus(ctx, changeID)
	if err != nil {
		return changeInfo{}, protocol.ErrInternalError
	}
	if !found {
		status = changeStatusInsync
	}
	return changeInfo{ID: "/change/" + changeID, Status: status, SubmittedAt: s.clk.Now()}, nil
}

// ─── Hosted zones ─────────────────────────────────────────────────────────────

// zoneIDFromInput accepts a hosted zone ID in bare (Z123) or path
// (/hostedzone/Z123) form and returns the canonical path form.
func zoneIDFromInput(id string) string {
	if strings.HasPrefix(id, "/hostedzone/") {
		return id
	}
	return "/hostedzone/" + id
}

// requireZone loads a zone or returns the modeled NoSuchHostedZone error.
func (s *Service) requireZone(ctx context.Context, zoneID string) (*HostedZone, *protocol.AWSError) {
	zone, found, err := s.getZone(ctx, zoneID)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, errNoSuchHostedZone(zoneID)
	}
	return zone, nil
}

// createZoneInput carries CreateHostedZone parameters.
type createZoneInput struct {
	Name            string
	CallerReference string
	Comment         string
	PrivateZone     bool
	VPC             *VPC
}

// createHostedZoneCore validates and creates a hosted zone together with its
// default NS + SOA record sets, exactly as real AWS does.
func (s *Service) createHostedZoneCore(ctx context.Context, in createZoneInput) (*HostedZone, changeInfo, *protocol.AWSError) {
	if in.Name == "" {
		return nil, changeInfo{}, errInvalidInput("Name is required")
	}
	if in.CallerReference == "" {
		return nil, changeInfo{}, errInvalidInput("CallerReference is required")
	}
	name := normalizeDNSName(in.Name)
	if !validDNSName(name) {
		return nil, changeInfo{}, errInvalidDomainName(in.Name)
	}

	existing, err := s.listAllZones(ctx)
	if err != nil {
		return nil, changeInfo{}, protocol.ErrInternalError
	}
	for _, z := range existing {
		if z.CallerReference == in.CallerReference {
			return nil, changeInfo{}, errHostedZoneAlreadyExists(in.CallerReference)
		}
	}

	bareID := newZoneID()
	zone := &HostedZone{
		Id:              "/hostedzone/" + bareID,
		Name:            name,
		CallerReference: in.CallerReference,
		CreatedAt:       s.clk.Now(),
		Comment:         in.Comment,
		PrivateZone:     in.PrivateZone,
		NameServers:     deriveNameServers(bareID),
	}
	if in.VPC != nil && in.VPC.VPCId != "" {
		zone.VPCs = []VPC{*in.VPC}
	}
	if err := s.putZone(ctx, zone); err != nil {
		return nil, changeInfo{}, protocol.ErrInternalError
	}
	for _, rr := range defaultRecordSets(zone) {
		if err := s.putRRSet(ctx, zone.Id, rr); err != nil {
			return nil, changeInfo{}, protocol.ErrInternalError
		}
	}
	change, aerr := s.recordChange(ctx)
	if aerr != nil {
		return nil, changeInfo{}, aerr
	}
	return zone, change, nil
}

// defaultRecordSets returns the apex NS and SOA record sets AWS creates with
// every hosted zone.
func defaultRecordSets(zone *HostedZone) []*ResourceRecordSet {
	servers := zone.nameServers()
	nsValues := make([]string, len(servers))
	for i, ns := range servers {
		nsValues[i] = ns + "."
	}
	return []*ResourceRecordSet{
		{Name: zone.Name, Type: "NS", TTL: 172800, ResourceRecords: nsValues},
		{Name: zone.Name, Type: "SOA", TTL: 900, ResourceRecords: []string{
			fmt.Sprintf("%s. awsdns-hostmaster.amazon.com. 1 7200 900 1209600 86400", servers[0]),
		}},
	}
}

// isDefaultRecordSet reports whether rr is one of the zone's required apex
// records (NS or SOA), which survive until the zone itself is deleted.
func isDefaultRecordSet(zone *HostedZone, rr *ResourceRecordSet) bool {
	return rr.Name == zone.Name && (rr.Type == "NS" || rr.Type == "SOA")
}

// getHostedZoneCore returns a zone plus its record set count.
func (s *Service) getHostedZoneCore(ctx context.Context, id string) (*HostedZone, int, *protocol.AWSError) {
	zone, aerr := s.requireZone(ctx, zoneIDFromInput(id))
	if aerr != nil {
		return nil, 0, aerr
	}
	count, err := s.countRRSets(ctx, zone.Id)
	if err != nil {
		return nil, 0, protocol.ErrInternalError
	}
	return zone, count, nil
}

// zonePage is one page of hosted zones with their record counts.
type zonePage struct {
	Zones       []*HostedZone
	Counts      map[string]int // zone ID (path form) → record set count
	IsTruncated bool
	// NextMarker / NextDNSName+NextHostedZoneId, depending on the operation.
	NextMarker string
	NextZone   *HostedZone
}

// sortedZones returns all zones ordered by the given key.
func (s *Service) sortedZones(ctx context.Context, less func(a, b *HostedZone) bool) ([]*HostedZone, map[string]int, *protocol.AWSError) {
	zones, err := s.listAllZones(ctx)
	if err != nil {
		return nil, nil, protocol.ErrInternalError
	}
	sort.Slice(zones, func(i, j int) bool { return less(zones[i], zones[j]) })
	counts, err := s.countRRSetsByZone(ctx)
	if err != nil {
		return nil, nil, protocol.ErrInternalError
	}
	return zones, counts, nil
}

// listHostedZonesCore pages zones by name order using AWS marker semantics:
// the marker is the bare ID of the first zone on the requested page.
func (s *Service) listHostedZonesCore(ctx context.Context, marker string, maxItems int) (zonePage, *protocol.AWSError) {
	zones, counts, aerr := s.sortedZones(ctx, func(a, b *HostedZone) bool {
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Id < b.Id
	})
	if aerr != nil {
		return zonePage{}, aerr
	}
	start := 0
	if marker != "" {
		start = slices.IndexFunc(zones, func(z *HostedZone) bool { return z.bareID() == marker })
		if start < 0 {
			return zonePage{}, errInvalidInput(fmt.Sprintf("Invalid request: The marker %s is not valid", marker))
		}
	}
	page := zonePage{Counts: counts}
	end := start + maxItems
	if end < len(zones) {
		page.IsTruncated = true
		page.NextMarker = zones[end].bareID()
	} else {
		end = len(zones)
	}
	page.Zones = zones[start:end]
	return page, nil
}

// listHostedZonesByNameCore pages zones in DNS order (labels reversed),
// starting at the optional dnsname/hostedzoneid position.
func (s *Service) listHostedZonesByNameCore(ctx context.Context, dnsname, hostedZoneID string, maxItems int) (zonePage, *protocol.AWSError) {
	zones, counts, aerr := s.sortedZones(ctx, func(a, b *HostedZone) bool {
		ka, kb := dnsSortKey(a.Name), dnsSortKey(b.Name)
		if ka != kb {
			return ka < kb
		}
		return a.bareID() < b.bareID()
	})
	if aerr != nil {
		return zonePage{}, aerr
	}
	start := 0
	if dnsname != "" {
		startKey := dnsSortKey(normalizeDNSName(dnsname))
		start = sort.Search(len(zones), func(i int) bool {
			key := dnsSortKey(zones[i].Name)
			if key != startKey {
				return key > startKey
			}
			return hostedZoneID == "" || zones[i].bareID() >= hostedZoneID
		})
	}
	page := zonePage{Counts: counts}
	end := start + maxItems
	if end < len(zones) {
		page.IsTruncated = true
		page.NextZone = zones[end]
	} else {
		end = len(zones)
	}
	page.Zones = zones[start:end]
	return page, nil
}

// countHostedZonesCore counts all hosted zones.
func (s *Service) countHostedZonesCore(ctx context.Context) (int, *protocol.AWSError) {
	zones, err := s.listAllZones(ctx)
	if err != nil {
		return 0, protocol.ErrInternalError
	}
	return len(zones), nil
}

// updateZoneCommentCore replaces a zone's comment.
func (s *Service) updateZoneCommentCore(ctx context.Context, id, comment string) (*HostedZone, int, *protocol.AWSError) {
	zone, aerr := s.requireZone(ctx, zoneIDFromInput(id))
	if aerr != nil {
		return nil, 0, aerr
	}
	zone.Comment = comment
	if err := s.putZone(ctx, zone); err != nil {
		return nil, 0, protocol.ErrInternalError
	}
	count, err := s.countRRSets(ctx, zone.Id)
	if err != nil {
		return nil, 0, protocol.ErrInternalError
	}
	return zone, count, nil
}

// deleteHostedZoneCore deletes a zone that holds no records beyond the
// default NS + SOA, cascading its record sets and tags.
func (s *Service) deleteHostedZoneCore(ctx context.Context, id string) (changeInfo, *protocol.AWSError) {
	zone, aerr := s.requireZone(ctx, zoneIDFromInput(id))
	if aerr != nil {
		return changeInfo{}, aerr
	}
	rrsets, err := s.listRRSets(ctx, zone.Id)
	if err != nil {
		return changeInfo{}, protocol.ErrInternalError
	}
	for _, rr := range rrsets {
		if !isDefaultRecordSet(zone, rr) {
			return changeInfo{}, errHostedZoneNotEmpty()
		}
	}
	if err := s.deleteZoneRRSets(ctx, zone.Id); err != nil {
		return changeInfo{}, protocol.ErrInternalError
	}
	if err := s.deleteTags(ctx, tagResourceHostedZone, zone.bareID()); err != nil {
		return changeInfo{}, protocol.ErrInternalError
	}
	if err := s.deleteZone(ctx, zone.Id); err != nil {
		return changeInfo{}, protocol.ErrInternalError
	}
	return s.recordChange(ctx)
}

// ─── Record sets ──────────────────────────────────────────────────────────────

// Change batch actions.
const (
	actionCreate = "CREATE"
	actionDelete = "DELETE"
	actionUpsert = "UPSERT"
)

// rrsetChange is one entry of a ChangeResourceRecordSets batch. HasTTL
// distinguishes an absent TTL from an explicit zero.
type rrsetChange struct {
	Action string
	RRSet  ResourceRecordSet
	HasTTL bool
}

// rrsetLabel renders the record identification AWS uses in InvalidChangeBatch
// messages: [name='x', type='A'] or [name='x', type='A', set-identifier='blue'].
func rrsetLabel(rr *ResourceRecordSet) string {
	if rr.SetIdentifier != "" {
		return fmt.Sprintf("[name='%s', type='%s', set-identifier='%s']", rr.Name, rr.Type, rr.SetIdentifier)
	}
	return fmt.Sprintf("[name='%s', type='%s']", rr.Name, rr.Type)
}

// changeWith renders the change identification AWS uses in InvalidInput
// messages about a specific change.
func changeWith(action string, rr *ResourceRecordSet) string {
	setID := rr.SetIdentifier
	if setID == "" {
		setID = "null"
	}
	return fmt.Sprintf("[Action=%s, Name=%s, Type=%s, SetIdentifier=%s]", action, rr.Name, rr.Type, setID)
}

// sameRecordData reports whether a DELETE's provided record data matches the
// stored record, per AWS's exact-match delete contract.
func sameRecordData(stored, provided *ResourceRecordSet) bool {
	if stored.isAlias() != provided.isAlias() {
		return false
	}
	if stored.isAlias() {
		return stored.AliasHostedZoneId == provided.AliasHostedZoneId &&
			strings.EqualFold(stored.AliasDNSName, provided.AliasDNSName) &&
			stored.AliasEvaluateTargetHealth == provided.AliasEvaluateTargetHealth
	}
	if stored.TTL != provided.TTL || len(stored.ResourceRecords) != len(provided.ResourceRecords) {
		return false
	}
	a := slices.Clone(stored.ResourceRecords)
	b := slices.Clone(provided.ResourceRecords)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// changeRRSetsCore validates a whole change batch against the zone's current
// records, then applies it. Validation is atomic: the first violation rejects
// the batch and nothing is written, matching AWS behaviour.
func (s *Service) changeRRSetsCore(ctx context.Context, zoneID string, changes []rrsetChange) (changeInfo, *protocol.AWSError) {
	zone, aerr := s.requireZone(ctx, zoneIDFromInput(zoneID))
	if aerr != nil {
		return changeInfo{}, aerr
	}
	if len(changes) == 0 {
		return changeInfo{}, errInvalidChangeBatch("The request contained no changes")
	}

	current, err := s.listRRSets(ctx, zone.Id)
	if err != nil {
		return changeInfo{}, protocol.ErrInternalError
	}
	working := make(map[string]*ResourceRecordSet, len(current))
	for _, rr := range current {
		working[rrsetKey("", rr.Name, rr.Type, rr.SetIdentifier)] = rr
	}

	// Pass 1: validate every change against a working copy so in-batch
	// sequencing (e.g. DELETE then CREATE) is judged like AWS judges it.
	for i := range changes {
		change := &changes[i]
		rr := &change.RRSet
		if change.Action != actionCreate && change.Action != actionDelete && change.Action != actionUpsert {
			return changeInfo{}, errEnumViolation(change.Action, "[CREATE, DELETE, UPSERT]")
		}
		if rr.Name == "" {
			return changeInfo{}, errInvalidInput("Name is required in ResourceRecordSet")
		}
		rr.Name = normalizeDNSName(rr.Name)
		if !validDNSName(rr.Name) {
			return changeInfo{}, errInvalidChangeBatch(fmt.Sprintf("The domain name %s is not valid", rr.Name))
		}
		if _, ok := rrTypes[rr.Type]; !ok {
			return changeInfo{}, errEnumViolation(rr.Type, "[SOA, A, TXT, NS, CNAME, MX, NAPTR, PTR, SRV, SPF, AAAA, CAA, DS, TLSA, SSHFP, SVCB, HTTPS]")
		}
		if !inZone(rr.Name, zone.Name) {
			return changeInfo{}, errInvalidChangeBatch(
				fmt.Sprintf("RRSet with DNS name %s is not permitted in zone %s", rr.Name, zone.Name))
		}
		if rr.Type == "CNAME" && rr.Name == zone.Name {
			return changeInfo{}, errInvalidChangeBatch(
				fmt.Sprintf("RRSet of type CNAME with DNS name %s is not permitted at apex in zone %s", rr.Name, zone.Name))
		}
		if aerr := validateRecordData(change); aerr != nil {
			return changeInfo{}, aerr
		}

		key := rrsetKey("", rr.Name, rr.Type, rr.SetIdentifier)
		switch change.Action {
		case actionCreate:
			if _, exists := working[key]; exists {
				return changeInfo{}, errInvalidChangeBatch(
					fmt.Sprintf("Tried to create resource record set %s but it already exists", rrsetLabel(rr)))
			}
			working[key] = rr
		case actionUpsert:
			working[key] = rr
		case actionDelete:
			if isDefaultRecordSet(zone, rr) {
				return changeInfo{}, errInvalidChangeBatch(fmt.Sprintf(
					"Tried to delete resource record set %s but it is the default NS or SOA resource record set for the hosted zone and cannot be deleted", rrsetLabel(rr)))
			}
			stored, exists := working[key]
			if !exists {
				return changeInfo{}, errInvalidChangeBatch(
					fmt.Sprintf("Tried to delete resource record set %s but it was not found", rrsetLabel(rr)))
			}
			if !sameRecordData(stored, rr) {
				return changeInfo{}, errInvalidChangeBatch(
					fmt.Sprintf("Tried to delete resource record set %s but the values provided do not match the current values", rrsetLabel(rr)))
			}
			delete(working, key)
		}
	}

	// Pass 2: apply. Validation succeeded for the whole batch.
	for i := range changes {
		change := &changes[i]
		var err error
		if change.Action == actionDelete {
			err = s.deleteRRSet(ctx, zone.Id, &change.RRSet)
		} else {
			err = s.putRRSet(ctx, zone.Id, &change.RRSet)
		}
		if err != nil {
			return changeInfo{}, protocol.ErrInternalError
		}
	}
	return s.recordChange(ctx)
}

// validateRecordData enforces AWS's "exactly one of alias or TTL+values"
// contract for a single change.
func validateRecordData(change *rrsetChange) *protocol.AWSError {
	rr := &change.RRSet
	hasValues := len(rr.ResourceRecords) > 0
	const expected = "Invalid request: Expected exactly one of [AliasTarget, all of [TTL, and ResourceRecords], or TrafficPolicyInstanceId], but found %s in Change with %s"
	if rr.isAlias() {
		if hasValues || change.HasTTL {
			return errInvalidInput(fmt.Sprintf(expected, "more than one", changeWith(change.Action, rr)))
		}
		return nil
	}
	if !hasValues || !change.HasTTL {
		return errInvalidInput(fmt.Sprintf(expected, "none", changeWith(change.Action, rr)))
	}
	return validateRecordValues(rr)
}

// validateRecordValues checks a record set's values against the grammar its
// type gives them, which is what stops `A` accepting a hostname.
//
// AWS reports each offending value on its own, in the aggregate InvalidChangeBatch
// envelope: "Invalid Resource Record: FATAL problem: ARRDATAIllegalIPv4Address
// (Value is not a valid IPv4 address) encountered with 'x'". The two IP codes are
// AWS's own; the rest follow the same shape, which is the approximation
// docs/services/route53/limitations.md already records for these messages.
//
// A type with no entry in rdataValidators keeps the shape-only check above —
// NAPTR, DS, TLSA, SSHFP, SVCB and HTTPS carry parameter grammars a local
// emulator gains nothing from policing, and SOA's RDATA is only ever minted by
// Overcast itself.
func validateRecordValues(rr *ResourceRecordSet) *protocol.AWSError {
	// AWS: "You can specify more than one value for all record types except
	// CNAME and SOA."
	if (rr.Type == "CNAME" || rr.Type == "SOA") && len(rr.ResourceRecords) > 1 {
		return errInvalidChangeBatch(fmt.Sprintf(
			"Invalid Resource Record: FATAL problem: %sRRSetMultipleRecords (Only one value is allowed for %s records) encountered with %s",
			rr.Type, rr.Type, rrsetLabel(rr)))
	}
	validate, modeled := rdataValidators[rr.Type]
	if !modeled {
		return nil
	}
	for _, value := range rr.ResourceRecords {
		if code, reason := validate(value); code != "" {
			return errInvalidChangeBatch(fmt.Sprintf(
				"Invalid Resource Record: FATAL problem: %s (%s) encountered with '%s'", code, reason, value))
		}
	}
	return nil
}

// rdataValidators maps a record type to the check its values must pass. Each
// returns AWS's problem code and its parenthesised reason, or two empty strings
// when the value is well formed.
var rdataValidators = map[string]func(string) (code, reason string){
	"A":     validateIPv4Value,
	"AAAA":  validateIPv6Value,
	"CNAME": hostnameValidator("CNAME"),
	"NS":    hostnameValidator("NS"),
	"PTR":   hostnameValidator("PTR"),
	"MX":    validateMXValue,
	"SRV":   validateSRVValue,
	"CAA":   validateCAAValue,
	"TXT":   characterStringValidator("TXT"),
	"SPF":   characterStringValidator("SPF"),
}

func validateIPv4Value(value string) (string, string) {
	// Is4 is false for the IPv4-in-IPv6 form (::ffff:1.2.3.4), which parses but
	// is not the dotted quad an A record takes.
	if ip, err := netip.ParseAddr(value); err != nil || !ip.Is4() {
		return "ARRDATAIllegalIPv4Address", "Value is not a valid IPv4 address"
	}
	return "", ""
}

func validateIPv6Value(value string) (string, string) {
	if ip, err := netip.ParseAddr(value); err != nil || ip.Is4() {
		return "AAAARRDATAIllegalIPv6Address", "Value is not a valid IPv6 address"
	}
	return "", ""
}

// hostnameValidator checks a bare domain name, the whole RDATA of CNAME, NS and
// PTR records.
func hostnameValidator(rrType string) func(string) (string, string) {
	return func(value string) (string, string) {
		if !validDNSName(normalizeDNSName(value)) {
			return rrType + "RRDATAIllegalHostName", "Value is not a valid host name"
		}
		return "", ""
	}
}

// validateMXValue checks "<preference> <exchange>".
func validateMXValue(value string) (string, string) {
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return "MXRRDATAIllegalFormat", "Value should be a number followed by a host name"
	}
	if !isUint16(fields[0]) {
		return "MXRRDATAIllegalPreference", "Preference should be a number between 0 and 65535"
	}
	if !validDNSName(normalizeDNSName(fields[1])) {
		return "MXRRDATAIllegalHostName", "Value is not a valid host name"
	}
	return "", ""
}

// validateSRVValue checks "<priority> <weight> <port> <target>".
func validateSRVValue(value string) (string, string) {
	fields := strings.Fields(value)
	if len(fields) != 4 {
		return "SRVRRDATAIllegalFormat", "Value should be three numbers followed by a host name"
	}
	for _, field := range fields[:3] {
		if !isUint16(field) {
			return "SRVRRDATAIllegalFormat", "Priority, weight and port should be numbers between 0 and 65535"
		}
	}
	if !validDNSName(normalizeDNSName(fields[3])) {
		return "SRVRRDATAIllegalHostName", "Value is not a valid host name"
	}
	return "", ""
}

// validateCAAValue checks `<flags> <tag> "<value>"`.
func validateCAAValue(value string) (string, string) {
	// The property value is a quoted string that may hold spaces, so it is
	// everything after the tag rather than a third whitespace-delimited field.
	flagsField, rest := cutField(value)
	tag, property := cutField(rest)
	if flagsField == "" || tag == "" || property == "" {
		return "CAARRDATAIllegalFormat", "Value should be a number, a tag and a quoted string"
	}
	flags, err := strconv.Atoi(flagsField)
	if err != nil || flags < 0 || flags > 255 {
		return "CAARRDATAIllegalFlags", "Flags should be a number between 0 and 255"
	}
	if strings.IndexFunc(tag, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) >= 0 {
		return "CAARRDATAIllegalTag", "Tag should be alphanumeric"
	}
	if code, reason := quotedStrings(property); code != "" {
		return "CAARRDATAIllegalValue", reason
	}
	return "", ""
}

// cutField splits off the first whitespace-delimited field, returning it and
// the remainder with the whitespace between them removed.
func cutField(value string) (field, rest string) {
	value = strings.TrimSpace(value)
	end := strings.IndexFunc(value, unicode.IsSpace)
	if end < 0 {
		return value, ""
	}
	return value[:end], strings.TrimSpace(value[end:])
}

// characterStringValidator checks the quoted character-strings TXT and SPF
// records are made of. AWS requires the quotes, and caps each string at 255
// characters — the DNS wire limit for one character-string.
func characterStringValidator(rrType string) func(string) (string, string) {
	return func(value string) (string, string) {
		if code, reason := quotedStrings(value); code != "" {
			return rrType + code, reason
		}
		return "", ""
	}
}

const unquoted = "Value should be enclosed in quotation marks"

// quotedStrings walks a sequence of space-separated quoted strings, returning
// the problem-code suffix and reason for the first thing wrong with it. A
// backslash escapes the character after it, so an embedded \" does not end the
// string — the escape AWS documents for TXT values.
func quotedStrings(value string) (suffix, reason string) {
	rest := strings.TrimSpace(value)
	if rest == "" {
		return "RRDATAIllegalCharacterString", unquoted
	}
	for rest != "" {
		if !strings.HasPrefix(rest, `"`) {
			return "RRDATAIllegalCharacterString", unquoted
		}
		length, end := 0, -1
		for i := 1; i < len(rest); i++ {
			if rest[i] == '\\' {
				i++
				length++
				continue
			}
			if rest[i] == '"' {
				end = i
				break
			}
			length++
		}
		if end < 0 {
			return "RRDATAIllegalCharacterString", unquoted
		}
		if length > 255 {
			return "RRDATAIllegalCharacterString", "Each quoted string may hold at most 255 characters"
		}
		rest = strings.TrimSpace(rest[end+1:])
	}
	return "", ""
}

func isUint16(field string) bool {
	n, err := strconv.Atoi(field)
	return err == nil && n >= 0 && n <= 65535
}

// rrsetPage is one page of a zone's record sets.
type rrsetPage struct {
	RRSets      []*ResourceRecordSet
	IsTruncated bool
	Next        *ResourceRecordSet // first record of the next page, when truncated
}

// listRRSetsCore pages a zone's record sets in DNS order, starting at the
// optional (name, type, identifier) position.
func (s *Service) listRRSetsCore(ctx context.Context, zoneID, startName, startType, startID string, maxItems int) (rrsetPage, *protocol.AWSError) {
	zone, aerr := s.requireZone(ctx, zoneIDFromInput(zoneID))
	if aerr != nil {
		return rrsetPage{}, aerr
	}
	rrsets, err := s.listRRSets(ctx, zone.Id) // already DNS-sorted
	if err != nil {
		return rrsetPage{}, protocol.ErrInternalError
	}
	start := 0
	if startName != "" {
		startKey := dnsSortKey(normalizeDNSName(startName))
		start = sort.Search(len(rrsets), func(i int) bool {
			key := dnsSortKey(rrsets[i].Name)
			if key != startKey {
				return key > startKey
			}
			if startType != "" && rrsets[i].Type != startType {
				return rrsets[i].Type > startType
			}
			return startID == "" || rrsets[i].SetIdentifier >= startID
		})
	}
	page := rrsetPage{}
	end := start + maxItems
	if end < len(rrsets) {
		page.IsTruncated = true
		page.Next = rrsets[end]
	} else {
		end = len(rrsets)
	}
	page.RRSets = rrsets[start:end]
	return page, nil
}

// ─── Tags ─────────────────────────────────────────────────────────────────────

// requireTaggableResource validates the resource type and the resource's
// existence, returning the modeled AWS error otherwise.
func (s *Service) requireTaggableResource(ctx context.Context, resourceType, bareID string) *protocol.AWSError {
	switch resourceType {
	case tagResourceHostedZone:
		_, aerr := s.requireZone(ctx, zoneIDFromInput(bareID))
		return aerr
	case tagResourceHealthCheck:
		_, found, err := s.getHealthCheck(ctx, bareID)
		if err != nil {
			return protocol.ErrInternalError
		}
		if !found {
			return errNoSuchHealthCheck(bareID)
		}
		return nil
	default:
		return errInvalidInput(fmt.Sprintf(
			"1 validation error detected: Value '%s' at 'resourceType' failed to satisfy constraint: Member must satisfy enum value set: [healthcheck, hostedzone]", resourceType))
	}
}

// changeTagsCore applies tag additions and removals to a resource.
func (s *Service) changeTagsCore(ctx context.Context, resourceType, bareID string, add []Tag, removeKeys []string) *protocol.AWSError {
	if aerr := s.requireTaggableResource(ctx, resourceType, bareID); aerr != nil {
		return aerr
	}
	tags, err := s.getTags(ctx, resourceType, bareID)
	if err != nil {
		return protocol.ErrInternalError
	}
	for _, tag := range add {
		tags[tag.Key] = tag.Value
	}
	for _, key := range removeKeys {
		delete(tags, key)
	}
	if err := s.putTags(ctx, resourceType, bareID, tags); err != nil {
		return protocol.ErrInternalError
	}
	return nil
}

// listTagsCore returns a resource's tags sorted by key.
func (s *Service) listTagsCore(ctx context.Context, resourceType, bareID string) ([]Tag, *protocol.AWSError) {
	if aerr := s.requireTaggableResource(ctx, resourceType, bareID); aerr != nil {
		return nil, aerr
	}
	tags, err := s.getTags(ctx, resourceType, bareID)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	return serviceutil.TagElements(tags, func(k, v string) Tag {
		return Tag{Key: k, Value: v}
	}), nil
}

// ─── Health checks ────────────────────────────────────────────────────────────

// createHealthCheckCore validates and creates a health check, applying AWS's
// documented defaults.
func (s *Service) createHealthCheckCore(ctx context.Context, callerRef string, cfg HealthCheckConfig) (*HealthCheck, *protocol.AWSError) {
	if callerRef == "" {
		return nil, errInvalidInput("CallerReference is required")
	}
	if cfg.Type == "" {
		return nil, errInvalidInput("Type is required in HealthCheckConfig")
	}
	if _, ok := healthCheckTypes[cfg.Type]; !ok {
		return nil, errEnumViolation(cfg.Type, "[HTTP, HTTPS, HTTP_STR_MATCH, HTTPS_STR_MATCH, TCP, CALCULATED, CLOUDWATCH_METRIC, RECOVERY_CONTROL]")
	}

	existing, err := s.listAllHealthChecks(ctx)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	for _, hc := range existing {
		if hc.CallerReference == callerRef {
			return nil, errHealthCheckAlreadyExists(callerRef)
		}
	}

	applyHealthCheckDefaults(&cfg)
	hc := &HealthCheck{
		Id:              uuid.NewString(),
		CallerReference: callerRef,
		CreatedAt:       s.clk.Now(),
		Version:         1,
		Config:          cfg,
	}
	if err := s.putHealthCheck(ctx, hc); err != nil {
		return nil, protocol.ErrInternalError
	}
	return hc, nil
}

// applyHealthCheckDefaults fills the defaults AWS documents for omitted
// health check configuration fields.
func applyHealthCheckDefaults(cfg *HealthCheckConfig) {
	if cfg.RequestInterval == 0 {
		cfg.RequestInterval = 30
	}
	if cfg.FailureThreshold == 0 {
		cfg.FailureThreshold = 3
	}
	if cfg.Port == 0 {
		switch cfg.Type {
		case "HTTP", "HTTP_STR_MATCH":
			cfg.Port = 80
		case "HTTPS", "HTTPS_STR_MATCH":
			cfg.Port = 443
		}
	}
}

// requireHealthCheck loads a health check or returns NoSuchHealthCheck.
func (s *Service) requireHealthCheck(ctx context.Context, id string) (*HealthCheck, *protocol.AWSError) {
	hc, found, err := s.getHealthCheck(ctx, id)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, errNoSuchHealthCheck(id)
	}
	return hc, nil
}

// healthCheckPage is one page of health checks.
type healthCheckPage struct {
	HealthChecks []*HealthCheck
	IsTruncated  bool
	NextMarker   string
}

// listHealthChecksCore pages health checks by ID using marker semantics.
func (s *Service) listHealthChecksCore(ctx context.Context, marker string, maxItems int) (healthCheckPage, *protocol.AWSError) {
	checks, err := s.listAllHealthChecks(ctx)
	if err != nil {
		return healthCheckPage{}, protocol.ErrInternalError
	}
	start := 0
	if marker != "" {
		start = slices.IndexFunc(checks, func(hc *HealthCheck) bool { return hc.Id == marker })
		if start < 0 {
			return healthCheckPage{}, errInvalidInput(fmt.Sprintf("Invalid request: The marker %s is not valid", marker))
		}
	}
	page := healthCheckPage{}
	end := start + maxItems
	if end < len(checks) {
		page.IsTruncated = true
		page.NextMarker = checks[end].Id
	} else {
		end = len(checks)
	}
	page.HealthChecks = checks[start:end]
	return page, nil
}

// updateHealthCheckInput carries UpdateHealthCheck parameters; nil pointer
// fields were absent from the request and leave the stored value unchanged.
type updateHealthCheckInput struct {
	HealthCheckVersion           *int64
	IPAddress                    *string
	Port                         *int
	ResourcePath                 *string
	FullyQualifiedDomainName     *string
	SearchString                 *string
	FailureThreshold             *int
	Inverted                     *bool
	Disabled                     *bool
	HealthThreshold              *int
	ChildHealthChecks            []string
	EnableSNI                    *bool
	Regions                      []string
	AlarmIdentifier              *AlarmIdentifier
	InsufficientDataHealthStatus *string
}

// updateHealthCheckCore applies an update, honouring the optional optimistic
// version lock, and bumps the health check version.
func (s *Service) updateHealthCheckCore(ctx context.Context, id string, in updateHealthCheckInput) (*HealthCheck, *protocol.AWSError) {
	hc, aerr := s.requireHealthCheck(ctx, id)
	if aerr != nil {
		return nil, aerr
	}
	if in.HealthCheckVersion != nil && *in.HealthCheckVersion != hc.Version {
		return nil, errHealthCheckVersionMismatch(id, *in.HealthCheckVersion, hc.Version)
	}
	cfg := &hc.Config
	setIf(in.IPAddress, &cfg.IPAddress)
	setIf(in.Port, &cfg.Port)
	setIf(in.ResourcePath, &cfg.ResourcePath)
	setIf(in.FullyQualifiedDomainName, &cfg.FullyQualifiedDomainName)
	setIf(in.SearchString, &cfg.SearchString)
	setIf(in.FailureThreshold, &cfg.FailureThreshold)
	setIf(in.InsufficientDataHealthStatus, &cfg.InsufficientDataHealthStatus)
	if in.Inverted != nil {
		cfg.Inverted = in.Inverted
	}
	if in.Disabled != nil {
		cfg.Disabled = in.Disabled
	}
	if in.HealthThreshold != nil {
		cfg.HealthThreshold = in.HealthThreshold
	}
	if in.ChildHealthChecks != nil {
		cfg.ChildHealthChecks = in.ChildHealthChecks
	}
	if in.EnableSNI != nil {
		cfg.EnableSNI = in.EnableSNI
	}
	if in.Regions != nil {
		cfg.Regions = in.Regions
	}
	if in.AlarmIdentifier != nil {
		cfg.AlarmIdentifier = in.AlarmIdentifier
	}
	hc.Version++
	if err := s.putHealthCheck(ctx, hc); err != nil {
		return nil, protocol.ErrInternalError
	}
	return hc, nil
}

// setIf copies a value when the request supplied one.
func setIf[T any](src *T, dst *T) {
	if src != nil {
		*dst = *src
	}
}

// deleteHealthCheckCore removes a health check and its tags.
func (s *Service) deleteHealthCheckCore(ctx context.Context, id string) *protocol.AWSError {
	if _, aerr := s.requireHealthCheck(ctx, id); aerr != nil {
		return aerr
	}
	if err := s.deleteTags(ctx, tagResourceHealthCheck, id); err != nil {
		return protocol.ErrInternalError
	}
	if err := s.deleteHealthCheck(ctx, id); err != nil {
		return protocol.ErrInternalError
	}
	return nil
}

// countHealthChecksCore counts all health checks.
func (s *Service) countHealthChecksCore(ctx context.Context) (int, *protocol.AWSError) {
	checks, err := s.listAllHealthChecks(ctx)
	if err != nil {
		return 0, protocol.ErrInternalError
	}
	return len(checks), nil
}
