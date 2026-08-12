package biz

import "testing"

func TestAdaptCustomerSubmitRequestUsesSingleProviderRoundForYangshi(t *testing.T) {
	items := []TaskItem{
		{ItemID: "item-1", PlatformCode: "deepseek", Seq: 1, Question: "question 1"},
		{ItemID: "item-2", PlatformCode: "doubao", Seq: 3, Question: "question 2"},
		{ItemID: "item-3", PlatformCode: "qwen", Seq: 0, Question: "question 3"},
	}

	for _, platformCode := range []string{"yangshi", "yangshi_dev"} {
		got := adaptCustomerSubmitRequest(&Task{PlatformCode: platformCode}, SubmitTaskPayload{Items: items})
		if len(got.Items) != len(items) {
			t.Fatalf("%s provider item count=%d, want %d", platformCode, len(got.Items), len(items))
		}
		for i, item := range got.Items {
			if item.Seq != 1 {
				t.Fatalf("%s provider item %d seq=%d, want 1", platformCode, i, item.Seq)
			}
		}
	}
	if items[1].Seq != 3 {
		t.Fatalf("original item seq=%d, want 3", items[1].Seq)
	}
}

func TestAdaptCustomerSubmitRequestPreservesSeqForNonYangshi(t *testing.T) {
	items := []TaskItem{
		{ItemID: "item-1", Seq: 1},
		{ItemID: "item-2", Seq: 3},
	}

	got := adaptCustomerSubmitRequest(&Task{PlatformCode: "mainland"}, SubmitTaskPayload{Items: items})
	if got.Items[0].Seq != 1 || got.Items[1].Seq != 3 {
		t.Fatalf("provider seqs=%d,%d; want 1,3", got.Items[0].Seq, got.Items[1].Seq)
	}
}
