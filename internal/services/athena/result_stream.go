package athena

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// result_stream.go — where a statement's rows go as it produces them: to the
// result store a chunk at a time, and to the result object in S3 as they are
// encoded. Nothing holds the whole result, so a query that returns millions
// of rows costs Overcast a chunk of them rather than all of them. A result
// larger than ATHENA_MAX_RESULT_BYTES fails its query.

// rowSink takes a statement's result rows in order, the header first.
type rowSink interface {
	// write takes the next row. An error, a *resultError, fails the
	// statement.
	write(row []*string) error
}

// resultError is a row the result could not take, and how the statement
// fails because of it.
type resultError struct{ fail *queryFailure }

func (e *resultError) Error() string { return e.fail.Reason }

// resultStream is the rowSink behind one query. The executor commits it when
// the statement succeeds, and aborts it otherwise.
type resultStream struct {
	// ctx outlives a cancel of the query, so a cancelled query's partial
	// result can still be removed and a finished one committed.
	ctx    context.Context
	id     string
	chunks *resultChunkWriter
	// output writes the result object at location; nil when results are not
	// written to S3.
	output   *resultWriter
	location string
	object   *resultUpload // opened by the first row
	// limit is the largest total length of the result's values; 0, which
	// only a Config built in a test holds, is none.
	limit, size int64
	committed   bool
	log         *serviceutil.ServiceLogger
}

func (s *resultStream) write(row []*string) error {
	if fail := s.add(row); fail != nil {
		return &resultError{fail}
	}
	return nil
}

// add passes row on to the result store and the result object.
func (s *resultStream) add(row []*string) *queryFailure {
	s.size += textLength(row)
	if s.limit > 0 && s.size > s.limit {
		return resultTooLarge(s.limit)
	}
	if err := s.chunks.write(s.ctx, row); err != nil {
		return s.storeFailure(err)
	}
	object, fail := s.opened()
	if fail != nil || object == nil {
		return fail
	}
	if aerr := object.write(row); aerr != nil {
		return resultWriteFailure(s.location, aerr)
	}
	return nil
}

// opened is the result object, opened on first use, or nil when results
// are not written.
func (s *resultStream) opened() (*resultUpload, *queryFailure) {
	if s.object == nil && s.output != nil {
		object, aerr := s.output.open(s.ctx, s.location)
		if aerr != nil {
			return nil, resultWriteFailure(s.location, aerr)
		}
		s.object = object
	}
	return s.object, nil
}

// commit keeps the result: its object is finished in S3, then res is stored
// as its header, for GetQueryResults. A result that could not be written is
// not kept.
func (s *resultStream) commit(res *queryResult) *queryFailure {
	object, fail := s.opened()
	if fail != nil {
		return fail
	}
	if object != nil {
		if aerr := object.close(s.ctx, res.Columns); aerr != nil {
			return resultWriteFailure(s.location, aerr)
		}
	}
	if err := s.chunks.close(s.ctx, res); err != nil {
		return s.storeFailure(err)
	}
	s.committed = true
	return nil
}

// abort removes what was written of a result that is not kept. After a
// commit it does nothing.
func (s *resultStream) abort() {
	if s.committed {
		return
	}
	if s.object != nil {
		s.object.abandon()
	}
	if err := s.chunks.discard(s.ctx); err != nil {
		s.log.WithRecorder(s.ctx).Warn("abandoned query result not deleted", zap.String("queryExecutionId", s.id), zap.Error(err))
	}
}

func (s *resultStream) storeFailure(err error) *queryFailure {
	s.log.WithRecorder(s.ctx).Error("query result not stored", zap.String("queryExecutionId", s.id), zap.Error(err))
	return failure(errorCategorySystem, errorTypeInternal, "Overcast could not store the query result.")
}

// resultTooLarge fails a query whose result passed ATHENA_MAX_RESULT_BYTES,
// a limit Athena does not have. ErrorType 0 is the catalog's nearest
// meaning, a query that exhausted its resources; unlike the engine running
// out of memory it is the user's error and not retryable, since the same
// query fails the same way until it returns less or the limit is raised.
func resultTooLarge(limit int64) *queryFailure {
	return failure(errorCategoryUser, errorTypeResourcesExhausted, fmt.Sprintf(
		"The query result is larger than Overcast's limit of %d bytes. Return fewer rows, or raise ATHENA_MAX_RESULT_BYTES.", limit))
}

// textLength is the total length of a row's values.
func textLength(row []*string) int64 {
	var n int64
	for _, cell := range row {
		if cell != nil {
			n += int64(len(*cell))
		}
	}
	return n
}
