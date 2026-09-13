package efs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Lifecycle states shared by file systems, mount targets, and access points.
const (
	stateCreating  = "creating"
	stateAvailable = "available"
	stateDeleting  = "deleting"
	// stateError is only reachable for mount targets, and only with the NFS
	// data plane on: an export container that never answered leaves the mount
	// target in the same state AWS reports for a mount target that failed to
	// come up, rather than claiming it is serving.
	stateError = "error"
)

// AWS-documented limits.
const (
	maxCreationTokenLength   = 64
	maxSecurityGroups        = 5
	maxAccessPointsPerFS     = 1000
	maxLifecyclePolicies     = 3
	minProvisionedThroughput = 1.0
	maxProvisionedThroughput = 3414.0
)

// emptyFileSystemSizeBytes is the metered size AWS reports for an empty file
// system (6 KiB of metadata).
const emptyFileSystemSizeBytes = 6144

// ─── Wire types ───────────────────────────────────────────────────────────────

// Tag is the EFS wire tag shape.
type Tag struct {
	Key   string `json:"Key" cbor:"Key"`
	Value string `json:"Value" cbor:"Value"`
}

// FileSystemSize mirrors the SizeInBytes member of FileSystemDescription.
type FileSystemSize struct {
	Value           int64   `json:"Value" cbor:"Value"`
	Timestamp       float64 `json:"Timestamp,omitempty" cbor:"Timestamp,omitempty"`
	ValueInIA       int64   `json:"ValueInIA" cbor:"ValueInIA"`
	ValueInStandard int64   `json:"ValueInStandard" cbor:"ValueInStandard"`
	ValueInArchive  int64   `json:"ValueInArchive" cbor:"ValueInArchive"`
}

// FileSystemProtectionDescription mirrors the FileSystemProtection member.
type FileSystemProtectionDescription struct {
	ReplicationOverwriteProtection string `json:"ReplicationOverwriteProtection,omitempty" cbor:"ReplicationOverwriteProtection,omitempty"`
}

// FileSystemDescription is the wire shape returned by CreateFileSystem,
// DescribeFileSystems, and UpdateFileSystem.
type FileSystemDescription struct {
	OwnerId                      string                           `json:"OwnerId" cbor:"OwnerId"`
	CreationToken                string                           `json:"CreationToken" cbor:"CreationToken"`
	FileSystemId                 string                           `json:"FileSystemId" cbor:"FileSystemId"`
	FileSystemArn                string                           `json:"FileSystemArn" cbor:"FileSystemArn"`
	CreationTime                 float64                          `json:"CreationTime" cbor:"CreationTime"`
	LifeCycleState               string                           `json:"LifeCycleState" cbor:"LifeCycleState"`
	Name                         string                           `json:"Name,omitempty" cbor:"Name,omitempty"`
	NumberOfMountTargets         int                              `json:"NumberOfMountTargets" cbor:"NumberOfMountTargets"`
	SizeInBytes                  FileSystemSize                   `json:"SizeInBytes" cbor:"SizeInBytes"`
	PerformanceMode              string                           `json:"PerformanceMode" cbor:"PerformanceMode"`
	Encrypted                    bool                             `json:"Encrypted" cbor:"Encrypted"`
	KmsKeyId                     string                           `json:"KmsKeyId,omitempty" cbor:"KmsKeyId,omitempty"`
	ThroughputMode               string                           `json:"ThroughputMode" cbor:"ThroughputMode"`
	ProvisionedThroughputInMibps *float64                         `json:"ProvisionedThroughputInMibps,omitempty" cbor:"ProvisionedThroughputInMibps,omitempty"`
	AvailabilityZoneName         string                           `json:"AvailabilityZoneName,omitempty" cbor:"AvailabilityZoneName,omitempty"`
	AvailabilityZoneId           string                           `json:"AvailabilityZoneId,omitempty" cbor:"AvailabilityZoneId,omitempty"`
	Tags                         []Tag                            `json:"Tags" cbor:"Tags"`
	FileSystemProtection         *FileSystemProtectionDescription `json:"FileSystemProtection,omitempty" cbor:"FileSystemProtection,omitempty"`
}

// MountTargetDescription is the wire shape returned by CreateMountTarget and
// DescribeMountTargets.
type MountTargetDescription struct {
	OwnerId              string `json:"OwnerId" cbor:"OwnerId"`
	MountTargetId        string `json:"MountTargetId" cbor:"MountTargetId"`
	FileSystemId         string `json:"FileSystemId" cbor:"FileSystemId"`
	SubnetId             string `json:"SubnetId" cbor:"SubnetId"`
	LifeCycleState       string `json:"LifeCycleState" cbor:"LifeCycleState"`
	IpAddress            string `json:"IpAddress,omitempty" cbor:"IpAddress,omitempty"`
	Ipv6Address          string `json:"Ipv6Address,omitempty" cbor:"Ipv6Address,omitempty"`
	NetworkInterfaceId   string `json:"NetworkInterfaceId,omitempty" cbor:"NetworkInterfaceId,omitempty"`
	AvailabilityZoneId   string `json:"AvailabilityZoneId,omitempty" cbor:"AvailabilityZoneId,omitempty"`
	AvailabilityZoneName string `json:"AvailabilityZoneName,omitempty" cbor:"AvailabilityZoneName,omitempty"`
}

// PosixUser is the POSIX identity applied through an access point.
type PosixUser struct {
	Uid           *int64  `json:"Uid" cbor:"Uid"`
	Gid           *int64  `json:"Gid" cbor:"Gid"`
	SecondaryGids []int64 `json:"SecondaryGids,omitempty" cbor:"SecondaryGids,omitempty"`
}

// CreationInfo describes directory-creation settings for an access point root.
type CreationInfo struct {
	OwnerUid    *int64 `json:"OwnerUid" cbor:"OwnerUid"`
	OwnerGid    *int64 `json:"OwnerGid" cbor:"OwnerGid"`
	Permissions string `json:"Permissions" cbor:"Permissions"`
}

// RootDirectory is the directory an access point exposes.
type RootDirectory struct {
	Path         string        `json:"Path,omitempty" cbor:"Path,omitempty"`
	CreationInfo *CreationInfo `json:"CreationInfo,omitempty" cbor:"CreationInfo,omitempty"`
}

// AccessPointDescription is the wire shape returned by CreateAccessPoint and
// DescribeAccessPoints.
type AccessPointDescription struct {
	ClientToken    string         `json:"ClientToken,omitempty" cbor:"ClientToken,omitempty"`
	Name           string         `json:"Name,omitempty" cbor:"Name,omitempty"`
	Tags           []Tag          `json:"Tags" cbor:"Tags"`
	AccessPointId  string         `json:"AccessPointId" cbor:"AccessPointId"`
	AccessPointArn string         `json:"AccessPointArn" cbor:"AccessPointArn"`
	FileSystemId   string         `json:"FileSystemId" cbor:"FileSystemId"`
	PosixUser      *PosixUser     `json:"PosixUser,omitempty" cbor:"PosixUser,omitempty"`
	RootDirectory  *RootDirectory `json:"RootDirectory,omitempty" cbor:"RootDirectory,omitempty"`
	OwnerId        string         `json:"OwnerId" cbor:"OwnerId"`
	LifeCycleState string         `json:"LifeCycleState" cbor:"LifeCycleState"`
}

// LifecyclePolicy is one entry of a lifecycle configuration.
type LifecyclePolicy struct {
	TransitionToIA                  string `json:"TransitionToIA,omitempty" cbor:"TransitionToIA,omitempty"`
	TransitionToPrimaryStorageClass string `json:"TransitionToPrimaryStorageClass,omitempty" cbor:"TransitionToPrimaryStorageClass,omitempty"`
	TransitionToArchive             string `json:"TransitionToArchive,omitempty" cbor:"TransitionToArchive,omitempty"`
}

// BackupPolicy is the automatic-backup switch of a file system.
type BackupPolicy struct {
	Status string `json:"Status" cbor:"Status"`
}

// ResourceIdPreference is the account-level resource-ID type setting.
type ResourceIdPreference struct {
	ResourceIdType string   `json:"ResourceIdType" cbor:"ResourceIdType"`
	Resources      []string `json:"Resources,omitempty" cbor:"Resources,omitempty"`
}

// ─── Errors ───────────────────────────────────────────────────────────────────
//
// EFS error codes and statuses follow the API reference:
// https://docs.aws.amazon.com/efs/latest/ug/API_Operations.html

func errBadRequest(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "BadRequest", Message: msg, HTTPStatus: http.StatusBadRequest}
}

func errFileSystemNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{Code: "FileSystemNotFound", Message: fmt.Sprintf("File system '%s' does not exist.", id), HTTPStatus: http.StatusNotFound}
}

func errFileSystemAlreadyExists(id string) *protocol.AWSError {
	return &protocol.AWSError{Code: "FileSystemAlreadyExists", Message: fmt.Sprintf("File system '%s' already exists with creation token.", id), HTTPStatus: http.StatusConflict}
}

func errFileSystemInUse(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "FileSystemInUse", Message: msg, HTTPStatus: http.StatusConflict}
}

func errIncorrectFileSystemLifeCycleState(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "IncorrectFileSystemLifeCycleState", Message: msg, HTTPStatus: http.StatusConflict}
}

func errMountTargetNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{Code: "MountTargetNotFound", Message: fmt.Sprintf("Mount target '%s' does not exist.", id), HTTPStatus: http.StatusNotFound}
}

func errMountTargetConflict(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "MountTargetConflict", Message: msg, HTTPStatus: http.StatusConflict}
}

func errIncorrectMountTargetState(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "IncorrectMountTargetState", Message: msg, HTTPStatus: http.StatusConflict}
}

func errAccessPointNotFound(id string) *protocol.AWSError {
	return &protocol.AWSError{Code: "AccessPointNotFound", Message: fmt.Sprintf("Access point '%s' does not exist.", id), HTTPStatus: http.StatusNotFound}
}

func errAccessPointAlreadyExists(id string) *protocol.AWSError {
	return &protocol.AWSError{Code: "AccessPointAlreadyExists", Message: fmt.Sprintf("Access point already exists with ID '%s'.", id), HTTPStatus: http.StatusConflict}
}

func errAccessPointLimitExceeded() *protocol.AWSError {
	return &protocol.AWSError{Code: "AccessPointLimitExceeded", Message: fmt.Sprintf("You have reached the maximum number of access points (%d) for this file system.", maxAccessPointsPerFS), HTTPStatus: http.StatusForbidden}
}

func errSecurityGroupLimitExceeded() *protocol.AWSError {
	return &protocol.AWSError{Code: "SecurityGroupLimitExceeded", Message: fmt.Sprintf("You cannot specify more than %d security groups.", maxSecurityGroups), HTTPStatus: http.StatusBadRequest}
}

func errPolicyNotFound(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "PolicyNotFound", Message: msg, HTTPStatus: http.StatusNotFound}
}

func errInvalidPolicy(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "InvalidPolicyException", Message: msg, HTTPStatus: http.StatusBadRequest}
}

// region resolves the request region from the handler context.
func (s *Service) region(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, s.cfg.Region)
}

// ─── File systems ─────────────────────────────────────────────────────────────

type createFileSystemRequest struct {
	CreationToken                string   `json:"CreationToken" cbor:"CreationToken"`
	PerformanceMode              string   `json:"PerformanceMode,omitempty" cbor:"PerformanceMode,omitempty"`
	Encrypted                    *bool    `json:"Encrypted,omitempty" cbor:"Encrypted,omitempty"`
	KmsKeyId                     string   `json:"KmsKeyId,omitempty" cbor:"KmsKeyId,omitempty"`
	ThroughputMode               string   `json:"ThroughputMode,omitempty" cbor:"ThroughputMode,omitempty"`
	ProvisionedThroughputInMibps *float64 `json:"ProvisionedThroughputInMibps,omitempty" cbor:"ProvisionedThroughputInMibps,omitempty"`
	AvailabilityZoneName         string   `json:"AvailabilityZoneName,omitempty" cbor:"AvailabilityZoneName,omitempty"`
	Backup                       *bool    `json:"Backup,omitempty" cbor:"Backup,omitempty"`
	Tags                         []Tag    `json:"Tags,omitempty" cbor:"Tags,omitempty"`
}

func (s *Service) createFileSystemTyped(ctx context.Context, req *createFileSystemRequest) (*FileSystemDescription, *protocol.AWSError) {
	region := s.region(ctx)

	token := strings.TrimSpace(req.CreationToken)
	if token == "" {
		return nil, errBadRequest("CreationToken must be provided.")
	}
	if len(token) > maxCreationTokenLength {
		return nil, errBadRequest(fmt.Sprintf("CreationToken must be between 1 and %d characters.", maxCreationTokenLength))
	}

	performanceMode := req.PerformanceMode
	if performanceMode == "" {
		performanceMode = "generalPurpose"
	}
	if performanceMode != "generalPurpose" && performanceMode != "maxIO" {
		return nil, errBadRequest(fmt.Sprintf("Invalid PerformanceMode: '%s'. Must be one of generalPurpose, maxIO.", req.PerformanceMode))
	}

	throughputMode := req.ThroughputMode
	if throughputMode == "" {
		throughputMode = "bursting"
	}
	switch throughputMode {
	case "bursting", "provisioned", "elastic":
	default:
		return nil, errBadRequest(fmt.Sprintf("Invalid ThroughputMode: '%s'. Must be one of bursting, provisioned, elastic.", req.ThroughputMode))
	}
	if aerr := validateThroughput(throughputMode, req.ProvisionedThroughputInMibps); aerr != nil {
		return nil, aerr
	}
	if performanceMode == "maxIO" && throughputMode == "elastic" {
		return nil, errBadRequest("The maxIO performance mode cannot be used with elastic throughput.")
	}
	if performanceMode == "maxIO" && req.AvailabilityZoneName != "" {
		return nil, errBadRequest("One Zone file systems do not support the maxIO performance mode.")
	}

	if existing, err := s.findFileSystemByCreationToken(ctx, region, token); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	} else if existing != nil {
		return nil, errFileSystemAlreadyExists(existing.FileSystemId)
	}

	backupStatus := "DISABLED"
	if req.Backup != nil && *req.Backup {
		backupStatus = "ENABLED"
	}

	rec := &fileSystemRecord{
		FileSystemId:                   newEFSID("fs-"),
		CreationToken:                  token,
		CreationTime:                   float64(s.clk.Now().Unix()),
		LifeCycleState:                 stateCreating,
		PerformanceMode:                performanceMode,
		Encrypted:                      req.Encrypted != nil && *req.Encrypted,
		KmsKeyId:                       req.KmsKeyId,
		ThroughputMode:                 throughputMode,
		ProvisionedThroughputInMibps:   req.ProvisionedThroughputInMibps,
		AvailabilityZoneName:           req.AvailabilityZoneName,
		BackupPolicyStatus:             backupStatus,
		ReplicationOverwriteProtection: "ENABLED",
	}
	if rec.AvailabilityZoneName != "" {
		rec.AvailabilityZoneId = azIDForName(region, rec.AvailabilityZoneName)
	}
	if err := s.putFileSystem(ctx, region, rec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if len(req.Tags) > 0 {
		if aerr := s.mergeTags(ctx, region, rec.FileSystemId, req.Tags); aerr != nil {
			return nil, aerr
		}
	}

	// Live mode: back the file system with a named Docker volume (no-op in
	// mock mode or while Docker is unavailable — see live_volumes.go).
	s.ensureVolume(ctx, rec.FileSystemId)

	// creating → available. Zero delay: inline with a real clock so the file
	// system is usable as soon as the create call returns, still observable as
	// "creating" under a mock clock (and in this response) — see
	// lifecycle.Scheduler.After.
	s.advanceFileSystem(region, rec.FileSystemId)

	return s.fileSystemDescription(ctx, region, rec), nil
}

// advanceFileSystem schedules the creating → available transition.
func (s *Service) advanceFileSystem(region, fsID string) {
	s.scheduler.AfterScoped(region, fsID, stateAvailable, 0, func(ctx context.Context) {
		rec, found, err := s.getFileSystem(ctx, region, fsID)
		if err != nil || !found || rec.LifeCycleState != stateCreating {
			return
		}
		rec.LifeCycleState = stateAvailable
		if err := s.putFileSystem(ctx, region, rec); err != nil {
			s.log.Error("efs: file system transition to available failed: " + fsID)
		}
	})
}

// fileSystemDescription builds the wire description for a record, resolving
// the Name tag and current mount-target count.
func (s *Service) fileSystemDescription(ctx context.Context, region string, rec *fileSystemRecord) *FileSystemDescription {
	mountTargets := 0
	if mts, err := s.listMountTargetsForFileSystem(ctx, region, rec.FileSystemId); err == nil {
		mountTargets = len(mts)
	}
	return s.fileSystemDescriptionWithCounts(ctx, region, rec, mountTargets)
}

// fileSystemDescriptionWithCounts is fileSystemDescription with the
// mount-target count precomputed, so list paths can amortize one scan across
// the whole page.
func (s *Service) fileSystemDescriptionWithCounts(ctx context.Context, region string, rec *fileSystemRecord, mountTargets int) *FileSystemDescription {
	tags := s.loadTags(ctx, region, rec.FileSystemId)
	if tags == nil {
		tags = []Tag{}
	}
	return &FileSystemDescription{
		OwnerId:              s.accountID(),
		CreationToken:        rec.CreationToken,
		FileSystemId:         rec.FileSystemId,
		FileSystemArn:        s.fileSystemARN(region, rec.FileSystemId),
		CreationTime:         rec.CreationTime,
		LifeCycleState:       rec.LifeCycleState,
		Name:                 nameFromTags(tags),
		NumberOfMountTargets: mountTargets,
		SizeInBytes: FileSystemSize{
			Value:           emptyFileSystemSizeBytes,
			Timestamp:       float64(s.clk.Now().Unix()),
			ValueInStandard: emptyFileSystemSizeBytes,
		},
		PerformanceMode:              rec.PerformanceMode,
		Encrypted:                    rec.Encrypted,
		KmsKeyId:                     rec.KmsKeyId,
		ThroughputMode:               rec.ThroughputMode,
		ProvisionedThroughputInMibps: rec.ProvisionedThroughputInMibps,
		AvailabilityZoneName:         rec.AvailabilityZoneName,
		AvailabilityZoneId:           rec.AvailabilityZoneId,
		Tags:                         tags,
		FileSystemProtection: &FileSystemProtectionDescription{
			ReplicationOverwriteProtection: rec.ReplicationOverwriteProtection,
		},
	}
}

type describeFileSystemsRequest struct {
	MaxItems      int    `json:"MaxItems,omitempty" cbor:"MaxItems,omitempty"`
	Marker        string `json:"Marker,omitempty" cbor:"Marker,omitempty"`
	CreationToken string `json:"CreationToken,omitempty" cbor:"CreationToken,omitempty"`
	FileSystemId  string `json:"FileSystemId,omitempty" cbor:"FileSystemId,omitempty"`
}

type describeFileSystemsResponse struct {
	Marker      string                   `json:"Marker,omitempty" cbor:"Marker,omitempty"`
	FileSystems []*FileSystemDescription `json:"FileSystems" cbor:"FileSystems"`
	NextMarker  string                   `json:"NextMarker,omitempty" cbor:"NextMarker,omitempty"`
}

func (s *Service) describeFileSystemsTyped(ctx context.Context, req *describeFileSystemsRequest) (*describeFileSystemsResponse, *protocol.AWSError) {
	region := s.region(ctx)
	if req.FileSystemId != "" && req.CreationToken != "" {
		return nil, errBadRequest("Provide either FileSystemId or CreationToken, not both.")
	}

	var records []*fileSystemRecord
	switch {
	case req.FileSystemId != "":
		rec, found, err := s.getFileSystem(ctx, region, req.FileSystemId)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		if !found {
			return nil, errFileSystemNotFound(req.FileSystemId)
		}
		records = []*fileSystemRecord{rec}
	case req.CreationToken != "":
		rec, err := s.findFileSystemByCreationToken(ctx, region, req.CreationToken)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		if rec == nil {
			return nil, errFileSystemNotFound(req.CreationToken)
		}
		records = []*fileSystemRecord{rec}
	default:
		var err error
		records, err = s.listFileSystems(ctx, region)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
	}

	page, err := serviceutil.Paginate(records, req.MaxItems, req.Marker, serviceutil.PaginateOptions{DefaultLimit: 100})
	if err != nil {
		return nil, errBadRequest("The provided Marker is not valid.")
	}
	// One mount-target scan for the whole page instead of one per file system.
	mountTargetCounts, err := s.countMountTargetsByFileSystem(ctx, region)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	out := make([]*FileSystemDescription, 0, len(page.Items))
	for _, rec := range page.Items {
		desc := s.fileSystemDescriptionWithCounts(ctx, region, rec, mountTargetCounts[rec.FileSystemId])
		out = append(out, desc)
	}
	return &describeFileSystemsResponse{
		Marker:      req.Marker,
		FileSystems: out,
		NextMarker:  page.NextToken,
	}, nil
}

type deleteFileSystemRequest struct {
	FileSystemId string `json:"FileSystemId" cbor:"FileSystemId"`
}

func (s *Service) deleteFileSystemTyped(ctx context.Context, req *deleteFileSystemRequest) (any, *protocol.AWSError) {
	region := s.region(ctx)
	rec, found, err := s.getFileSystem(ctx, region, req.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	mts, err := s.listMountTargetsForFileSystem(ctx, region, rec.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if len(mts) > 0 {
		return nil, errFileSystemInUse(fmt.Sprintf("File system '%s' has mount targets. Delete all mount targets before deleting the file system.", rec.FileSystemId))
	}

	rec.LifeCycleState = stateDeleting
	if err := s.putFileSystem(ctx, region, rec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.scheduler.CancelScoped(region, rec.FileSystemId, stateAvailable)
	fsID := rec.FileSystemId
	s.scheduler.AfterScoped(region, fsID, "deleted", 0, func(ctx context.Context) {
		// Access points belonging to the file system are removed with it.
		if aps, err := s.listAccessPointsForFileSystem(ctx, region, fsID); err == nil {
			for _, ap := range aps {
				_ = deleteRecord(ctx, s, nsAccessPoints, region, ap.AccessPointId)
				_ = s.saveTags(ctx, region, ap.AccessPointId, nil)
			}
		}
		_ = s.deleteFileSystemPolicyRecord(ctx, region, fsID)
		_ = s.saveTags(ctx, region, fsID, nil)
		if err := deleteRecord(ctx, s, nsFileSystems, region, fsID); err != nil {
			s.log.Error("efs: file system deletion failed: " + fsID)
		}
		s.removeVolume(ctx, fsID)
	})
	return nil, nil
}

type updateFileSystemRequest struct {
	FileSystemId                 string   `json:"FileSystemId" cbor:"FileSystemId"`
	ThroughputMode               string   `json:"ThroughputMode,omitempty" cbor:"ThroughputMode,omitempty"`
	ProvisionedThroughputInMibps *float64 `json:"ProvisionedThroughputInMibps,omitempty" cbor:"ProvisionedThroughputInMibps,omitempty"`
}

func (s *Service) updateFileSystemTyped(ctx context.Context, req *updateFileSystemRequest) (*FileSystemDescription, *protocol.AWSError) {
	region := s.region(ctx)
	rec, found, err := s.getFileSystem(ctx, region, req.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	if rec.LifeCycleState != stateAvailable {
		return nil, errIncorrectFileSystemLifeCycleState(fmt.Sprintf("File system '%s' is in state '%s'; it must be 'available' to be updated.", rec.FileSystemId, rec.LifeCycleState))
	}

	mode := req.ThroughputMode
	if mode == "" {
		mode = rec.ThroughputMode
	}
	switch mode {
	case "bursting", "provisioned", "elastic":
	default:
		return nil, errBadRequest(fmt.Sprintf("Invalid ThroughputMode: '%s'. Must be one of bursting, provisioned, elastic.", req.ThroughputMode))
	}
	throughput := req.ProvisionedThroughputInMibps
	if throughput == nil && mode == "provisioned" {
		throughput = rec.ProvisionedThroughputInMibps
	}
	if aerr := validateThroughput(mode, throughput); aerr != nil {
		return nil, aerr
	}
	if rec.PerformanceMode == "maxIO" && mode == "elastic" {
		return nil, errBadRequest("The maxIO performance mode cannot be used with elastic throughput.")
	}

	rec.ThroughputMode = mode
	if mode == "provisioned" {
		rec.ProvisionedThroughputInMibps = throughput
	} else {
		rec.ProvisionedThroughputInMibps = nil
	}
	if err := s.putFileSystem(ctx, region, rec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return s.fileSystemDescription(ctx, region, rec), nil
}

// validateThroughput enforces the ThroughputMode ↔ ProvisionedThroughputInMibps
// pairing rules shared by CreateFileSystem and UpdateFileSystem.
func validateThroughput(mode string, throughput *float64) *protocol.AWSError {
	if mode == "provisioned" {
		if throughput == nil {
			return errBadRequest("ProvisionedThroughputInMibps must be provided when ThroughputMode is 'provisioned'.")
		}
		if *throughput < minProvisionedThroughput || *throughput > maxProvisionedThroughput {
			return errBadRequest(fmt.Sprintf("ProvisionedThroughputInMibps must be between %g and %g.", minProvisionedThroughput, maxProvisionedThroughput))
		}
		return nil
	}
	if throughput != nil {
		return errBadRequest("ProvisionedThroughputInMibps can only be provided when ThroughputMode is 'provisioned'.")
	}
	return nil
}

type updateFileSystemProtectionRequest struct {
	FileSystemId                   string `json:"FileSystemId" cbor:"FileSystemId"`
	ReplicationOverwriteProtection string `json:"ReplicationOverwriteProtection,omitempty" cbor:"ReplicationOverwriteProtection,omitempty"`
}

func (s *Service) updateFileSystemProtectionTyped(ctx context.Context, req *updateFileSystemProtectionRequest) (*FileSystemProtectionDescription, *protocol.AWSError) {
	region := s.region(ctx)
	rec, found, err := s.getFileSystem(ctx, region, req.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	switch req.ReplicationOverwriteProtection {
	case "ENABLED", "DISABLED":
	default:
		return nil, errBadRequest(fmt.Sprintf("Invalid ReplicationOverwriteProtection: '%s'. Must be one of ENABLED, DISABLED.", req.ReplicationOverwriteProtection))
	}
	rec.ReplicationOverwriteProtection = req.ReplicationOverwriteProtection
	if err := s.putFileSystem(ctx, region, rec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &FileSystemProtectionDescription{ReplicationOverwriteProtection: rec.ReplicationOverwriteProtection}, nil
}

// ─── Mount targets ────────────────────────────────────────────────────────────

type createMountTargetRequest struct {
	FileSystemId   string   `json:"FileSystemId" cbor:"FileSystemId"`
	SubnetId       string   `json:"SubnetId" cbor:"SubnetId"`
	IpAddress      string   `json:"IpAddress,omitempty" cbor:"IpAddress,omitempty"`
	Ipv6Address    string   `json:"Ipv6Address,omitempty" cbor:"Ipv6Address,omitempty"`
	SecurityGroups []string `json:"SecurityGroups,omitempty" cbor:"SecurityGroups,omitempty"`
}

func (s *Service) createMountTargetTyped(ctx context.Context, req *createMountTargetRequest) (*MountTargetDescription, *protocol.AWSError) {
	region := s.region(ctx)
	if req.FileSystemId == "" {
		return nil, errBadRequest("FileSystemId must be provided.")
	}
	if req.SubnetId == "" {
		return nil, errBadRequest("SubnetId must be provided.")
	}
	if len(req.SecurityGroups) > maxSecurityGroups {
		return nil, errSecurityGroupLimitExceeded()
	}

	fs, found, err := s.getFileSystem(ctx, region, req.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	if fs.LifeCycleState != stateAvailable {
		return nil, errIncorrectFileSystemLifeCycleState(fmt.Sprintf("File system '%s' is in state '%s'; it must be 'available' to create mount targets.", fs.FileSystemId, fs.LifeCycleState))
	}

	azName, azID := s.zoneForSubnet(ctx, region, req.SubnetId)
	if fs.AvailabilityZoneName != "" && fs.AvailabilityZoneName != azName {
		// One Zone file systems only accept mount targets in their own zone.
		azName, azID = fs.AvailabilityZoneName, fs.AvailabilityZoneId
	}
	existing, err := s.listMountTargetsForFileSystem(ctx, region, fs.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	for _, mt := range existing {
		if mt.SubnetId == req.SubnetId || mt.AvailabilityZoneName == azName {
			return nil, errMountTargetConflict(fmt.Sprintf("mount target already exists in this AZ (%s)", azName))
		}
	}
	// The export has to be placeable in the subnet's VPC before the mount
	// target exists, the way RDS refuses an instance in an unlaunchable VPC:
	// failing the create is honest, minting a mount target nothing in the VPC
	// can reach is not.
	if aerr := s.refuseUnlaunchableVPC(ctx, req.SubnetId); aerr != nil {
		return nil, aerr
	}

	rec := &mountTargetRecord{
		MountTargetId:        newEFSID("fsmt-"),
		FileSystemId:         fs.FileSystemId,
		SubnetId:             req.SubnetId,
		LifeCycleState:       stateCreating,
		IpAddress:            req.IpAddress,
		Ipv6Address:          req.Ipv6Address,
		NetworkInterfaceId:   newEFSID("eni-"),
		AvailabilityZoneName: azName,
		AvailabilityZoneId:   azID,
		SecurityGroups:       req.SecurityGroups,
	}
	if rec.IpAddress == "" {
		rec.IpAddress = syntheticIPForMountTarget(rec.MountTargetId)
	}
	if err := s.putMountTarget(ctx, region, rec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}

	mtID := rec.MountTargetId
	if s.nfsActive() {
		// The NFS data plane owns the transition: the mount target becomes
		// available once its export answers (or once the readiness retries run
		// out). Starting the container can involve an image pull, so it never
		// runs on the request path.
		s.startExportAsync(region, mtID, fs.FileSystemId, rec.SubnetId)
	} else {
		s.scheduler.AfterScoped(region, mtID, stateAvailable, 0, func(ctx context.Context) {
			mt, found, err := s.getMountTarget(ctx, region, mtID)
			if err != nil || !found || mt.LifeCycleState != stateCreating {
				return
			}
			mt.LifeCycleState = stateAvailable
			if err := s.putMountTarget(ctx, region, mt); err != nil {
				s.log.Error("efs: mount target transition to available failed: " + mtID)
			}
		})
	}

	return s.mountTargetDescription(rec), nil
}

func (s *Service) mountTargetDescription(rec *mountTargetRecord) *MountTargetDescription {
	return &MountTargetDescription{
		OwnerId:              s.accountID(),
		MountTargetId:        rec.MountTargetId,
		FileSystemId:         rec.FileSystemId,
		SubnetId:             rec.SubnetId,
		LifeCycleState:       rec.LifeCycleState,
		IpAddress:            rec.IpAddress,
		Ipv6Address:          rec.Ipv6Address,
		NetworkInterfaceId:   rec.NetworkInterfaceId,
		AvailabilityZoneId:   rec.AvailabilityZoneId,
		AvailabilityZoneName: rec.AvailabilityZoneName,
	}
}

type describeMountTargetsRequest struct {
	MaxItems      int    `json:"MaxItems,omitempty" cbor:"MaxItems,omitempty"`
	Marker        string `json:"Marker,omitempty" cbor:"Marker,omitempty"`
	FileSystemId  string `json:"FileSystemId,omitempty" cbor:"FileSystemId,omitempty"`
	MountTargetId string `json:"MountTargetId,omitempty" cbor:"MountTargetId,omitempty"`
	AccessPointId string `json:"AccessPointId,omitempty" cbor:"AccessPointId,omitempty"`
}

type describeMountTargetsResponse struct {
	Marker       string                    `json:"Marker,omitempty" cbor:"Marker,omitempty"`
	MountTargets []*MountTargetDescription `json:"MountTargets" cbor:"MountTargets"`
	NextMarker   string                    `json:"NextMarker,omitempty" cbor:"NextMarker,omitempty"`
}

func (s *Service) describeMountTargetsTyped(ctx context.Context, req *describeMountTargetsRequest) (*describeMountTargetsResponse, *protocol.AWSError) {
	region := s.region(ctx)

	var records []*mountTargetRecord
	switch {
	case req.MountTargetId != "":
		rec, found, err := s.getMountTarget(ctx, region, req.MountTargetId)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		if !found {
			return nil, errMountTargetNotFound(req.MountTargetId)
		}
		records = []*mountTargetRecord{rec}
	case req.AccessPointId != "":
		ap, found, err := s.getAccessPoint(ctx, region, req.AccessPointId)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		if !found {
			return nil, errAccessPointNotFound(req.AccessPointId)
		}
		records, err = s.listMountTargetsForFileSystem(ctx, region, ap.FileSystemId)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
	case req.FileSystemId != "":
		if _, found, err := s.getFileSystem(ctx, region, req.FileSystemId); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		} else if !found {
			return nil, errFileSystemNotFound(req.FileSystemId)
		}
		var err error
		records, err = s.listMountTargetsForFileSystem(ctx, region, req.FileSystemId)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
	default:
		return nil, errBadRequest("One of FileSystemId, MountTargetId, or AccessPointId must be provided.")
	}

	page, err := serviceutil.Paginate(records, req.MaxItems, req.Marker, serviceutil.PaginateOptions{DefaultLimit: 10})
	if err != nil {
		return nil, errBadRequest("The provided Marker is not valid.")
	}
	out := make([]*MountTargetDescription, 0, len(page.Items))
	for _, rec := range page.Items {
		out = append(out, s.mountTargetDescription(rec))
	}
	return &describeMountTargetsResponse{
		Marker:       req.Marker,
		MountTargets: out,
		NextMarker:   page.NextToken,
	}, nil
}

type deleteMountTargetRequest struct {
	MountTargetId string `json:"MountTargetId" cbor:"MountTargetId"`
}

func (s *Service) deleteMountTargetTyped(ctx context.Context, req *deleteMountTargetRequest) (any, *protocol.AWSError) {
	region := s.region(ctx)
	rec, found, err := s.getMountTarget(ctx, region, req.MountTargetId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errMountTargetNotFound(req.MountTargetId)
	}

	rec.LifeCycleState = stateDeleting
	if err := s.putMountTarget(ctx, region, rec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.scheduler.CancelScoped(region, rec.MountTargetId, stateAvailable)
	s.scheduler.CancelScoped(region, rec.MountTargetId, "nfs-ready")
	// Tear the export down from the record we still hold: it carries the only
	// copy of the container ID. A start still in flight finds the record
	// "deleting" (or gone) and cleans up its own container — see recordExport.
	s.stopExportAsync(rec)
	mtID := rec.MountTargetId
	s.scheduler.AfterScoped(region, mtID, "deleted", 0, func(ctx context.Context) {
		if err := deleteRecord(ctx, s, nsMountTargets, region, mtID); err != nil {
			s.log.Error("efs: mount target deletion failed: " + mtID)
		}
	})
	return nil, nil
}

type mountTargetSecurityGroupsRequest struct {
	MountTargetId  string   `json:"MountTargetId" cbor:"MountTargetId"`
	SecurityGroups []string `json:"SecurityGroups,omitempty" cbor:"SecurityGroups,omitempty"`
}

type describeMountTargetSecurityGroupsResponse struct {
	SecurityGroups []string `json:"SecurityGroups" cbor:"SecurityGroups"`
}

func (s *Service) describeMountTargetSecurityGroupsTyped(ctx context.Context, req *mountTargetSecurityGroupsRequest) (*describeMountTargetSecurityGroupsResponse, *protocol.AWSError) {
	region := s.region(ctx)
	rec, found, err := s.getMountTarget(ctx, region, req.MountTargetId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errMountTargetNotFound(req.MountTargetId)
	}
	groups := rec.SecurityGroups
	if groups == nil {
		groups = []string{}
	}
	return &describeMountTargetSecurityGroupsResponse{SecurityGroups: groups}, nil
}

func (s *Service) modifyMountTargetSecurityGroupsTyped(ctx context.Context, req *mountTargetSecurityGroupsRequest) (any, *protocol.AWSError) {
	region := s.region(ctx)
	if len(req.SecurityGroups) > maxSecurityGroups {
		return nil, errSecurityGroupLimitExceeded()
	}
	rec, found, err := s.getMountTarget(ctx, region, req.MountTargetId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errMountTargetNotFound(req.MountTargetId)
	}
	if rec.LifeCycleState == stateDeleting {
		return nil, errIncorrectMountTargetState(fmt.Sprintf("Mount target '%s' is being deleted.", rec.MountTargetId))
	}
	rec.SecurityGroups = req.SecurityGroups
	if err := s.putMountTarget(ctx, region, rec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil, nil
}

// ─── Access points ────────────────────────────────────────────────────────────

type createAccessPointRequest struct {
	ClientToken   string         `json:"ClientToken" cbor:"ClientToken"`
	Tags          []Tag          `json:"Tags,omitempty" cbor:"Tags,omitempty"`
	FileSystemId  string         `json:"FileSystemId" cbor:"FileSystemId"`
	PosixUser     *PosixUser     `json:"PosixUser,omitempty" cbor:"PosixUser,omitempty"`
	RootDirectory *RootDirectory `json:"RootDirectory,omitempty" cbor:"RootDirectory,omitempty"`
}

func (s *Service) createAccessPointTyped(ctx context.Context, req *createAccessPointRequest) (*AccessPointDescription, *protocol.AWSError) {
	region := s.region(ctx)
	if req.ClientToken == "" {
		return nil, errBadRequest("ClientToken must be provided.")
	}
	if req.FileSystemId == "" {
		return nil, errBadRequest("FileSystemId must be provided.")
	}
	if req.PosixUser != nil && (req.PosixUser.Uid == nil || req.PosixUser.Gid == nil) {
		return nil, errBadRequest("PosixUser must specify both Uid and Gid.")
	}

	fs, found, err := s.getFileSystem(ctx, region, req.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	if fs.LifeCycleState != stateAvailable {
		return nil, errIncorrectFileSystemLifeCycleState(fmt.Sprintf("File system '%s' is in state '%s'; it must be 'available' to create access points.", fs.FileSystemId, fs.LifeCycleState))
	}

	existing, err := s.listAccessPointsForFileSystem(ctx, region, fs.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	for _, ap := range existing {
		if ap.ClientToken == req.ClientToken {
			return nil, errAccessPointAlreadyExists(ap.AccessPointId)
		}
	}
	if len(existing) >= maxAccessPointsPerFS {
		return nil, errAccessPointLimitExceeded()
	}

	rootDir := req.RootDirectory
	if rootDir == nil || rootDir.Path == "" {
		if rootDir == nil {
			rootDir = &RootDirectory{}
		}
		rootDir.Path = "/"
	}

	rec := &accessPointRecord{
		AccessPointId:  newEFSID("fsap-"),
		ClientToken:    req.ClientToken,
		FileSystemId:   fs.FileSystemId,
		LifeCycleState: stateCreating,
		PosixUser:      req.PosixUser,
		RootDirectory:  rootDir,
	}
	if err := s.putAccessPoint(ctx, region, rec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if len(req.Tags) > 0 {
		if aerr := s.mergeTags(ctx, region, rec.AccessPointId, req.Tags); aerr != nil {
			return nil, aerr
		}
	}

	apID := rec.AccessPointId
	s.scheduler.AfterScoped(region, apID, stateAvailable, 0, func(ctx context.Context) {
		ap, found, err := s.getAccessPoint(ctx, region, apID)
		if err != nil || !found || ap.LifeCycleState != stateCreating {
			return
		}
		ap.LifeCycleState = stateAvailable
		if err := s.putAccessPoint(ctx, region, ap); err != nil {
			s.log.Error("efs: access point transition to available failed: " + apID)
		}
	})

	return s.accessPointDescription(ctx, region, rec), nil
}

func (s *Service) accessPointDescription(ctx context.Context, region string, rec *accessPointRecord) *AccessPointDescription {
	tags := s.loadTags(ctx, region, rec.AccessPointId)
	if tags == nil {
		tags = []Tag{}
	}
	return &AccessPointDescription{
		ClientToken:    rec.ClientToken,
		Name:           nameFromTags(tags),
		Tags:           tags,
		AccessPointId:  rec.AccessPointId,
		AccessPointArn: s.accessPointARN(region, rec.AccessPointId),
		FileSystemId:   rec.FileSystemId,
		PosixUser:      rec.PosixUser,
		RootDirectory:  rec.RootDirectory,
		OwnerId:        s.accountID(),
		LifeCycleState: rec.LifeCycleState,
	}
}

type describeAccessPointsRequest struct {
	MaxResults    int    `json:"MaxResults,omitempty" cbor:"MaxResults,omitempty"`
	NextToken     string `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
	AccessPointId string `json:"AccessPointId,omitempty" cbor:"AccessPointId,omitempty"`
	FileSystemId  string `json:"FileSystemId,omitempty" cbor:"FileSystemId,omitempty"`
}

type describeAccessPointsResponse struct {
	AccessPoints []*AccessPointDescription `json:"AccessPoints" cbor:"AccessPoints"`
	NextToken    string                    `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
}

func (s *Service) describeAccessPointsTyped(ctx context.Context, req *describeAccessPointsRequest) (*describeAccessPointsResponse, *protocol.AWSError) {
	region := s.region(ctx)
	if req.AccessPointId != "" && req.FileSystemId != "" {
		return nil, errBadRequest("Provide either AccessPointId or FileSystemId, not both.")
	}

	var records []*accessPointRecord
	switch {
	case req.AccessPointId != "":
		rec, found, err := s.getAccessPoint(ctx, region, req.AccessPointId)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		if !found {
			return nil, errAccessPointNotFound(req.AccessPointId)
		}
		records = []*accessPointRecord{rec}
	case req.FileSystemId != "":
		if _, found, err := s.getFileSystem(ctx, region, req.FileSystemId); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		} else if !found {
			return nil, errFileSystemNotFound(req.FileSystemId)
		}
		var err error
		records, err = s.listAccessPointsForFileSystem(ctx, region, req.FileSystemId)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
	default:
		var err error
		records, err = s.listAccessPoints(ctx, region)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
	}

	page, err := serviceutil.Paginate(records, req.MaxResults, req.NextToken, serviceutil.PaginateOptions{DefaultLimit: 100})
	if err != nil {
		return nil, errBadRequest("The provided NextToken is not valid.")
	}
	out := make([]*AccessPointDescription, 0, len(page.Items))
	for _, rec := range page.Items {
		out = append(out, s.accessPointDescription(ctx, region, rec))
	}
	return &describeAccessPointsResponse{AccessPoints: out, NextToken: page.NextToken}, nil
}

type deleteAccessPointRequest struct {
	AccessPointId string `json:"AccessPointId" cbor:"AccessPointId"`
}

func (s *Service) deleteAccessPointTyped(ctx context.Context, req *deleteAccessPointRequest) (any, *protocol.AWSError) {
	region := s.region(ctx)
	rec, found, err := s.getAccessPoint(ctx, region, req.AccessPointId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errAccessPointNotFound(req.AccessPointId)
	}

	rec.LifeCycleState = stateDeleting
	if err := s.putAccessPoint(ctx, region, rec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.scheduler.CancelScoped(region, rec.AccessPointId, stateAvailable)
	apID := rec.AccessPointId
	s.scheduler.AfterScoped(region, apID, "deleted", 0, func(ctx context.Context) {
		_ = s.saveTags(ctx, region, apID, nil)
		if err := deleteRecord(ctx, s, nsAccessPoints, region, apID); err != nil {
			s.log.Error("efs: access point deletion failed: " + apID)
		}
	})
	return nil, nil
}

// ─── Lifecycle configuration ──────────────────────────────────────────────────

type putLifecycleConfigurationRequest struct {
	FileSystemId      string            `json:"FileSystemId" cbor:"FileSystemId"`
	LifecyclePolicies []LifecyclePolicy `json:"LifecyclePolicies" cbor:"LifecyclePolicies"`
}

type lifecycleConfigurationResponse struct {
	LifecyclePolicies []LifecyclePolicy `json:"LifecyclePolicies" cbor:"LifecyclePolicies"`
}

var validIATransitions = map[string]bool{
	"AFTER_7_DAYS": true, "AFTER_14_DAYS": true, "AFTER_30_DAYS": true,
	"AFTER_60_DAYS": true, "AFTER_90_DAYS": true, "AFTER_1_DAY": true,
	"AFTER_180_DAYS": true, "AFTER_270_DAYS": true, "AFTER_365_DAYS": true,
}

func (s *Service) putLifecycleConfigurationTyped(ctx context.Context, req *putLifecycleConfigurationRequest) (*lifecycleConfigurationResponse, *protocol.AWSError) {
	region := s.region(ctx)
	rec, found, err := s.getFileSystem(ctx, region, req.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	if len(req.LifecyclePolicies) > maxLifecyclePolicies {
		return nil, errBadRequest(fmt.Sprintf("LifecyclePolicies can contain at most %d policies.", maxLifecyclePolicies))
	}
	for _, p := range req.LifecyclePolicies {
		set := 0
		if p.TransitionToIA != "" {
			if !validIATransitions[p.TransitionToIA] {
				return nil, errBadRequest(fmt.Sprintf("Invalid TransitionToIA value: '%s'.", p.TransitionToIA))
			}
			set++
		}
		if p.TransitionToPrimaryStorageClass != "" {
			if p.TransitionToPrimaryStorageClass != "AFTER_1_ACCESS" {
				return nil, errBadRequest(fmt.Sprintf("Invalid TransitionToPrimaryStorageClass value: '%s'.", p.TransitionToPrimaryStorageClass))
			}
			set++
		}
		if p.TransitionToArchive != "" {
			if !validIATransitions[p.TransitionToArchive] {
				return nil, errBadRequest(fmt.Sprintf("Invalid TransitionToArchive value: '%s'.", p.TransitionToArchive))
			}
			set++
		}
		if set != 1 {
			return nil, errBadRequest("Each LifecyclePolicy must specify exactly one transition.")
		}
	}
	rec.LifecyclePolicies = req.LifecyclePolicies
	if err := s.putFileSystem(ctx, region, rec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &lifecycleConfigurationResponse{LifecyclePolicies: lifecyclePoliciesOrEmpty(rec)}, nil
}

type describeLifecycleConfigurationRequest struct {
	FileSystemId string `json:"FileSystemId" cbor:"FileSystemId"`
}

func (s *Service) describeLifecycleConfigurationTyped(ctx context.Context, req *describeLifecycleConfigurationRequest) (*lifecycleConfigurationResponse, *protocol.AWSError) {
	region := s.region(ctx)
	rec, found, err := s.getFileSystem(ctx, region, req.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	return &lifecycleConfigurationResponse{LifecyclePolicies: lifecyclePoliciesOrEmpty(rec)}, nil
}

func lifecyclePoliciesOrEmpty(rec *fileSystemRecord) []LifecyclePolicy {
	if rec.LifecyclePolicies == nil {
		return []LifecyclePolicy{}
	}
	return rec.LifecyclePolicies
}

// ─── Backup policy ────────────────────────────────────────────────────────────

type putBackupPolicyRequest struct {
	FileSystemId string        `json:"FileSystemId" cbor:"FileSystemId"`
	BackupPolicy *BackupPolicy `json:"BackupPolicy" cbor:"BackupPolicy"`
}

type backupPolicyResponse struct {
	BackupPolicy BackupPolicy `json:"BackupPolicy" cbor:"BackupPolicy"`
}

func (s *Service) putBackupPolicyTyped(ctx context.Context, req *putBackupPolicyRequest) (*backupPolicyResponse, *protocol.AWSError) {
	region := s.region(ctx)
	rec, found, err := s.getFileSystem(ctx, region, req.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	if req.BackupPolicy == nil || (req.BackupPolicy.Status != "ENABLED" && req.BackupPolicy.Status != "DISABLED") {
		return nil, errBadRequest("BackupPolicy.Status must be ENABLED or DISABLED.")
	}
	rec.BackupPolicyStatus = req.BackupPolicy.Status
	if err := s.putFileSystem(ctx, region, rec); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &backupPolicyResponse{BackupPolicy: BackupPolicy{Status: rec.BackupPolicyStatus}}, nil
}

type describeBackupPolicyRequest struct {
	FileSystemId string `json:"FileSystemId" cbor:"FileSystemId"`
}

func (s *Service) describeBackupPolicyTyped(ctx context.Context, req *describeBackupPolicyRequest) (*backupPolicyResponse, *protocol.AWSError) {
	region := s.region(ctx)
	rec, found, err := s.getFileSystem(ctx, region, req.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	status := rec.BackupPolicyStatus
	if status == "" {
		status = "DISABLED"
	}
	return &backupPolicyResponse{BackupPolicy: BackupPolicy{Status: status}}, nil
}

// ─── File-system policy ───────────────────────────────────────────────────────

type putFileSystemPolicyRequest struct {
	FileSystemId                   string `json:"FileSystemId" cbor:"FileSystemId"`
	Policy                         string `json:"Policy" cbor:"Policy"`
	BypassPolicyLockoutSafetyCheck bool   `json:"BypassPolicyLockoutSafetyCheck,omitempty" cbor:"BypassPolicyLockoutSafetyCheck,omitempty"`
}

type fileSystemPolicyResponse struct {
	FileSystemId string `json:"FileSystemId" cbor:"FileSystemId"`
	Policy       string `json:"Policy" cbor:"Policy"`
}

func (s *Service) putFileSystemPolicyTyped(ctx context.Context, req *putFileSystemPolicyRequest) (*fileSystemPolicyResponse, *protocol.AWSError) {
	region := s.region(ctx)
	rec, found, err := s.getFileSystem(ctx, region, req.FileSystemId)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	if strings.TrimSpace(req.Policy) == "" {
		return nil, errInvalidPolicy("Policy must be provided.")
	}
	if !json.Valid([]byte(req.Policy)) {
		return nil, errInvalidPolicy("Policy is not valid JSON.")
	}
	if err := s.saveFileSystemPolicy(ctx, region, rec.FileSystemId, req.Policy); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &fileSystemPolicyResponse{FileSystemId: rec.FileSystemId, Policy: req.Policy}, nil
}

type describeFileSystemPolicyRequest struct {
	FileSystemId string `json:"FileSystemId" cbor:"FileSystemId"`
}

func (s *Service) describeFileSystemPolicyTyped(ctx context.Context, req *describeFileSystemPolicyRequest) (*fileSystemPolicyResponse, *protocol.AWSError) {
	region := s.region(ctx)
	if _, found, err := s.getFileSystem(ctx, region, req.FileSystemId); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	} else if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	policy, found := s.loadFileSystemPolicy(ctx, region, req.FileSystemId)
	if !found {
		return nil, errPolicyNotFound(fmt.Sprintf("No file system policy found for file system '%s'.", req.FileSystemId))
	}
	return &fileSystemPolicyResponse{FileSystemId: req.FileSystemId, Policy: policy}, nil
}

func (s *Service) deleteFileSystemPolicyTyped(ctx context.Context, req *describeFileSystemPolicyRequest) (any, *protocol.AWSError) {
	region := s.region(ctx)
	if _, found, err := s.getFileSystem(ctx, region, req.FileSystemId); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	} else if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	if err := s.deleteFileSystemPolicyRecord(ctx, region, req.FileSystemId); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil, nil
}

// ─── Tagging ──────────────────────────────────────────────────────────────────

// resolveTaggableResource verifies the resource ID names an existing file
// system or access point and returns the bare ID.
func (s *Service) resolveTaggableResource(ctx context.Context, region, idOrARN string) (string, *protocol.AWSError) {
	id := resourceIDFromARN(idOrARN)
	switch {
	case strings.HasPrefix(id, "fs-"):
		if _, found, err := s.getFileSystem(ctx, region, id); err != nil {
			return "", protocol.Wrap(protocol.ErrInternalError, err)
		} else if !found {
			return "", errFileSystemNotFound(id)
		}
	case strings.HasPrefix(id, "fsap-"):
		if _, found, err := s.getAccessPoint(ctx, region, id); err != nil {
			return "", protocol.Wrap(protocol.ErrInternalError, err)
		} else if !found {
			return "", errAccessPointNotFound(id)
		}
	default:
		return "", errBadRequest(fmt.Sprintf("Invalid resource ID: '%s'.", idOrARN))
	}
	return id, nil
}

type tagResourceRequest struct {
	ResourceId string `json:"ResourceId" cbor:"ResourceId"`
	Tags       []Tag  `json:"Tags" cbor:"Tags"`
}

func (s *Service) tagResourceTyped(ctx context.Context, req *tagResourceRequest) (any, *protocol.AWSError) {
	region := s.region(ctx)
	id, aerr := s.resolveTaggableResource(ctx, region, req.ResourceId)
	if aerr != nil {
		return nil, aerr
	}
	if len(req.Tags) == 0 {
		return nil, errBadRequest("Tags must be provided.")
	}
	for _, t := range req.Tags {
		if t.Key == "" {
			return nil, errBadRequest("Tag keys must not be empty.")
		}
	}
	if aerr := s.mergeTags(ctx, region, id, req.Tags); aerr != nil {
		return nil, aerr
	}
	return nil, nil
}

type untagResourceRequest struct {
	ResourceId string   `json:"ResourceId" cbor:"ResourceId"`
	TagKeys    []string `json:"TagKeys" cbor:"TagKeys"`
}

func (s *Service) untagResourceTyped(ctx context.Context, req *untagResourceRequest) (any, *protocol.AWSError) {
	region := s.region(ctx)
	id, aerr := s.resolveTaggableResource(ctx, region, req.ResourceId)
	if aerr != nil {
		return nil, aerr
	}
	if len(req.TagKeys) == 0 {
		return nil, errBadRequest("TagKeys must be provided.")
	}
	if err := s.removeTags(ctx, region, id, req.TagKeys); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil, nil
}

type listTagsForResourceRequest struct {
	ResourceId string `json:"ResourceId" cbor:"ResourceId"`
	MaxResults int    `json:"MaxResults,omitempty" cbor:"MaxResults,omitempty"`
	NextToken  string `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
}

type listTagsForResourceResponse struct {
	Tags      []Tag  `json:"Tags" cbor:"Tags"`
	NextToken string `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
}

func (s *Service) listTagsForResourceTyped(ctx context.Context, req *listTagsForResourceRequest) (*listTagsForResourceResponse, *protocol.AWSError) {
	region := s.region(ctx)
	id, aerr := s.resolveTaggableResource(ctx, region, req.ResourceId)
	if aerr != nil {
		return nil, aerr
	}
	tags := s.loadTags(ctx, region, id)
	page, err := serviceutil.Paginate(tags, req.MaxResults, req.NextToken, serviceutil.PaginateOptions{DefaultLimit: 100})
	if err != nil {
		return nil, errBadRequest("The provided NextToken is not valid.")
	}
	items := page.Items
	if items == nil {
		items = []Tag{}
	}
	return &listTagsForResourceResponse{Tags: items, NextToken: page.NextToken}, nil
}

// Legacy tagging API (CreateTags / DeleteTags / DescribeTags) — file systems only.

type createTagsRequest struct {
	FileSystemId string `json:"FileSystemId" cbor:"FileSystemId"`
	Tags         []Tag  `json:"Tags" cbor:"Tags"`
}

func (s *Service) createTagsTyped(ctx context.Context, req *createTagsRequest) (any, *protocol.AWSError) {
	return s.tagResourceTyped(ctx, &tagResourceRequest{ResourceId: req.FileSystemId, Tags: req.Tags})
}

type deleteTagsRequest struct {
	FileSystemId string   `json:"FileSystemId" cbor:"FileSystemId"`
	TagKeys      []string `json:"TagKeys" cbor:"TagKeys"`
}

func (s *Service) deleteTagsTyped(ctx context.Context, req *deleteTagsRequest) (any, *protocol.AWSError) {
	return s.untagResourceTyped(ctx, &untagResourceRequest{ResourceId: req.FileSystemId, TagKeys: req.TagKeys})
}

type describeTagsRequest struct {
	FileSystemId string `json:"FileSystemId" cbor:"FileSystemId"`
	MaxItems     int    `json:"MaxItems,omitempty" cbor:"MaxItems,omitempty"`
	Marker       string `json:"Marker,omitempty" cbor:"Marker,omitempty"`
}

type describeTagsResponse struct {
	Marker     string `json:"Marker,omitempty" cbor:"Marker,omitempty"`
	Tags       []Tag  `json:"Tags" cbor:"Tags"`
	NextMarker string `json:"NextMarker,omitempty" cbor:"NextMarker,omitempty"`
}

func (s *Service) describeTagsTyped(ctx context.Context, req *describeTagsRequest) (*describeTagsResponse, *protocol.AWSError) {
	region := s.region(ctx)
	if _, found, err := s.getFileSystem(ctx, region, req.FileSystemId); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	} else if !found {
		return nil, errFileSystemNotFound(req.FileSystemId)
	}
	tags := s.loadTags(ctx, region, req.FileSystemId)
	page, err := serviceutil.Paginate(tags, req.MaxItems, req.Marker, serviceutil.PaginateOptions{DefaultLimit: 100})
	if err != nil {
		return nil, errBadRequest("The provided Marker is not valid.")
	}
	items := page.Items
	if items == nil {
		items = []Tag{}
	}
	return &describeTagsResponse{Marker: req.Marker, Tags: items, NextMarker: page.NextToken}, nil
}

// ─── Account preferences ──────────────────────────────────────────────────────

type describeAccountPreferencesRequest struct {
	NextToken  string `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
	MaxResults int    `json:"MaxResults,omitempty" cbor:"MaxResults,omitempty"`
}

type accountPreferencesResponse struct {
	ResourceIdPreference ResourceIdPreference `json:"ResourceIdPreference" cbor:"ResourceIdPreference"`
	NextToken            string               `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
}

func (s *Service) describeAccountPreferencesTyped(ctx context.Context, _ *describeAccountPreferencesRequest) (*accountPreferencesResponse, *protocol.AWSError) {
	region := s.region(ctx)
	pref := ResourceIdPreference{ResourceIdType: "LONG_ID", Resources: []string{"FILE_SYSTEM", "MOUNT_TARGET"}}
	if raw, found, err := s.store.Get(ctx, nsPreferences, region); err == nil && found {
		_ = json.Unmarshal([]byte(raw), &pref)
	}
	return &accountPreferencesResponse{ResourceIdPreference: pref}, nil
}

type putAccountPreferencesRequest struct {
	ResourceIdType string `json:"ResourceIdType" cbor:"ResourceIdType"`
}

func (s *Service) putAccountPreferencesTyped(ctx context.Context, req *putAccountPreferencesRequest) (*accountPreferencesResponse, *protocol.AWSError) {
	region := s.region(ctx)
	if req.ResourceIdType != "LONG_ID" && req.ResourceIdType != "SHORT_ID" {
		return nil, errBadRequest("ResourceIdType must be LONG_ID or SHORT_ID.")
	}
	pref := ResourceIdPreference{ResourceIdType: req.ResourceIdType, Resources: []string{"FILE_SYSTEM", "MOUNT_TARGET"}}
	raw, err := json.Marshal(pref)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.store.Set(ctx, nsPreferences, region, string(raw)); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &accountPreferencesResponse{ResourceIdPreference: pref}, nil
}
