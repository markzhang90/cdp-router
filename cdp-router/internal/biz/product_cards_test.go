package biz

import (
	"encoding/json"
	"testing"
)

func TestProductCardsPreserveLegacyPullHash(t *testing.T) {
	// Frozen from the pre-product-card request representation, including field order.
	const legacy = `{"requestId":"legacy-request","batchNo":"legacy-batch","shardId":"7","schemaVersion":"1","finished":true,"results":[{"itemId":"q1","answerSeq":1,"executionStatus":"SUCCESS","errorCode":"","errorMessage":"","answerText":"legacy answer","thinkingProcess":"","generatedAt":"","recommendedQuestions":[],"references":[],"deviceId":"","recordingUrl":""}]}`
	const want = "78e109a711e37e350bdbc246820c7bf673be58fdd22560f15afd06af01053b62"
	var input PullSubmissionInput
	if err := json.Unmarshal([]byte(legacy), &input); err != nil {
		t.Fatal(err)
	}
	for _, cards := range [][]ProductCard{nil, {}} {
		input.Results[0].ProductCards = cards
		if got := pullHash(input); got != want {
			t.Fatalf("legacy hash changed: %s", got)
		}
	}
	input.Results[0].ProductCards = []ProductCard{{ProductName: "商品", SalesVolume: "1万+"}}
	first := pullHash(input)
	if first == want {
		t.Fatal("cards not included in request hash")
	}
	input.Results[0].ProductCards[0].SalesVolume = "2万+"
	if pullHash(input) == first {
		t.Fatal("card modification must conflict for the same request ID")
	}
}
