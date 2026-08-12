package biz

import "strings"

type customerSubmitRequestAdapter interface {
	Match(task *Task) bool
	RewriteSubmitPayload(task *Task, payload SubmitTaskPayload) SubmitTaskPayload
}

var customerSubmitRequestAdapters = []customerSubmitRequestAdapter{
	yangshiCustomerAdapter{},
}

func adaptCustomerSubmitRequest(task *Task, payload SubmitTaskPayload) SubmitTaskPayload {
	for _, adapter := range customerSubmitRequestAdapters {
		if adapter.Match(task) {
			payload = adapter.RewriteSubmitPayload(task, payload)
		}
	}
	return payload
}

type yangshiCustomerAdapter struct{}

func (yangshiCustomerAdapter) Match(task *Task) bool {
	if task == nil {
		return false
	}
	platformCode := strings.ToLower(strings.TrimSpace(task.PlatformCode))
	return platformCode == "yangshi" || strings.HasPrefix(platformCode, "yangshi_")
}

func (yangshiCustomerAdapter) RewriteSubmitPayload(_ *Task, payload SubmitTaskPayload) SubmitTaskPayload {
	payload.Items = singleProviderRoundItems(payload.Items)
	return payload
}

// mpzdata uses items[].seq as the execution count, while router seq is an
// internal round/order marker. This integration submits one execution per item.
func singleProviderRoundItems(items []TaskItem) []TaskItem {
	providerItems := make([]TaskItem, len(items))
	copy(providerItems, items)
	for i := range providerItems {
		providerItems[i].Seq = 1
	}
	return providerItems
}
