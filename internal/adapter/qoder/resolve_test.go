package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveBaseURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		configured string
		token      string
		want       string
	}{
		{name: "empty derives dt host", configured: "", token: "dt-device-token", want: QoderChatBase},
		{name: "empty derives jt host", configured: "", token: "jt-job-token", want: QoderChatBaseAlt},
		{name: "pinned api3 keeps dt on api3", configured: QoderChatBase, token: "dt-x", want: QoderChatBase},
		{name: "pinned api3 reroutes jt to api2", configured: QoderChatBase, token: "jt-x", want: QoderChatBaseAlt},
		{name: "pinned api2 keeps jt on api2", configured: QoderChatBaseAlt, token: "jt-x", want: QoderChatBaseAlt},
		{name: "pinned api2 reroutes dt to api3", configured: QoderChatBaseAlt, token: "dt-x", want: QoderChatBase},
		{name: "trailing slash tolerated", configured: QoderChatBase + "/", token: "jt-x", want: QoderChatBaseAlt},
		{name: "custom host honored", configured: "https://qoder.internal.example", token: "jt-x", want: "https://qoder.internal.example"},
		{name: "mock host honored", configured: "http://127.0.0.1:9", token: "dt-x", want: "http://127.0.0.1:9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, ResolveBaseURL(tc.configured, tc.token))
		})
	}
}
