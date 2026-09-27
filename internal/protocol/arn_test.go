package protocol_test

import (
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/protocol"
)

func TestARN_s3_omitsRegionAndAccount(t *testing.T) {
	got := protocol.ARN("us-east-1", "123456789012", "s3", "my-bucket")
	want := "arn:aws:s3:::my-bucket"
	if got != want {
		t.Errorf("ARN s3: expected %q, got %q", want, got)
	}
}

func TestARN_sqs(t *testing.T) {
	got := protocol.ARN("us-east-1", "000000000000", "sqs", "my-queue")
	want := "arn:aws:sqs:us-east-1:000000000000:my-queue"
	if got != want {
		t.Errorf("ARN sqs: expected %q, got %q", want, got)
	}
}

func TestQueueARN(t *testing.T) {
	got := protocol.QueueARN("eu-west-1", "123456789012", "my-queue")
	if !strings.Contains(got, "sqs") {
		t.Errorf("QueueARN: expected SQS ARN, got %q", got)
	}
	if !strings.HasSuffix(got, ":my-queue") {
		t.Errorf("QueueARN: expected suffix ':my-queue', got %q", got)
	}
}

func TestTopicARN(t *testing.T) {
	got := protocol.TopicARN("us-east-1", "000000000000", "my-topic")
	want := "arn:aws:sns:us-east-1:000000000000:my-topic"
	if got != want {
		t.Errorf("TopicARN: expected %q, got %q", want, got)
	}
}

func TestLambdaARN(t *testing.T) {
	got := protocol.LambdaARN("us-west-2", "111111111111", "my-func")
	want := "arn:aws:lambda:us-west-2:111111111111:function:my-func"
	if got != want {
		t.Errorf("LambdaARN: expected %q, got %q", want, got)
	}
}

func TestTableARN(t *testing.T) {
	got := protocol.TableARN("ap-southeast-1", "222222222222", "my-table")
	want := "arn:aws:dynamodb:ap-southeast-1:222222222222:table/my-table"
	if got != want {
		t.Errorf("TableARN: expected %q, got %q", want, got)
	}
}

// TestAssumedRoleARN verifies an assumed-role session ARN is built from the
// role name parsed out of the role ARN, without the role's path, and not from
// the session name repeated in both segments — see
// https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumedRoleUser.html.
func TestAssumedRoleARN(t *testing.T) {
	tests := []struct {
		name        string
		roleArn     string
		sessionName string
		want        string
	}{
		{
			name:        "simple role",
			roleArn:     "arn:aws:iam::123456789012:role/demo",
			sessionName: "Bob",
			want:        "arn:aws:sts::123456789012:assumed-role/demo/Bob",
		},
		{
			name:        "role arn with a path",
			roleArn:     "arn:aws:iam::123456789012:role/some/nested/path/MyRole",
			sessionName: "test-session",
			want:        "arn:aws:sts::123456789012:assumed-role/MyRole/test-session",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given: a role ARN and a session name
			// When: the session's ARN is built
			got := protocol.AssumedRoleARN("123456789012", tt.roleArn, tt.sessionName)

			// Then: it names the role by its name alone, then the session
			if got != tt.want {
				t.Errorf("AssumedRoleARN(%q, %q) = %q, want %q", tt.roleArn, tt.sessionName, got, tt.want)
			}
		})
	}
}
