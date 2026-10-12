package biz

import "testing"

func TestDeviceCapabilityFiltering(t *testing.T) {
	providers := []ProviderSettings{{ProviderCode: "legacy", Enabled: true}, {ProviderCode: "mobile", Enabled: true, InteractionMode: "pull", SupportedDevices: []string{"mobile", "web"}, DeviceChannels: map[string][]string{"mobile": {"doubao"}}}}
	for _, tc := range []struct {
		device, channel string
		count           int
	}{{"web", "new-channel", 2}, {"mobile", "doubao", 1}, {"mobile", "qianwen", 0}} {
		got := FilterProviders(providers, tc.device, []TaskItem{{PlatformCode: tc.channel}})
		if len(got) != tc.count {
			t.Fatalf("%s/%s providers=%v", tc.device, tc.channel, got)
		}
	}
}
func TestPullCompletionCannotReopenDelivery(t *testing.T) {
	for _, status := range []TaskStatus{TaskStatusSubmitted, TaskStatusDone, TaskStatusCallbackPending, TaskStatusReassigned, TaskStatusFailed} {
		if got := PullCompletionStatus(status, false, true); got != status {
			t.Fatalf("incomplete moved %s to %s", status, got)
		}
	}
	if PullCompletionStatus(TaskStatusSubmitted, true, false) != TaskStatusResultImported {
		t.Fatal("risk bypassed")
	}
	if PullCompletionStatus(TaskStatusSubmitted, true, true) != TaskStatusCallbackPending {
		t.Fatal("ready not queued")
	}
	if PullCompletionStatus(TaskStatusDone, true, true) != TaskStatusDone {
		t.Fatal("DONE correction reopened callback")
	}
}
