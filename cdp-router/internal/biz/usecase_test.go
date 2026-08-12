package biz

import "testing"

func TestOwnOSSObjectKeyDecodesEncodedPath(t *testing.T) {
	usecase := NewTaskUsecase(nil, nil, nil, nil, nil, nil, TaskUsecaseOptions{
		OSSPublicHosts: []string{
			"https://opensource-man.oss-cn-shanghai.aliyuncs.com",
			"opensource-man.oss-cn-shanghai.aliyuncs.com",
		},
	})

	got, ok := usecase.ownOSSObjectKey("https://opensource-man.oss-cn-shanghai.aliyuncs.com/cdp-router%2Fq2a%2Fmpzdata%2FQ2A4-20260730-mpzdata2-2d1d0193%2F356a6b65-%E6%95%B0%E6%8D%AE%E9%87%87%E9%9B%86_2026-07-30T08-20-24.xlsx")
	if !ok {
		t.Fatal("expected URL to match own OSS host")
	}
	want := "cdp-router/q2a/mpzdata/Q2A4-20260730-mpzdata2-2d1d0193/356a6b65-数据采集_2026-07-30T08-20-24.xlsx"
	if got != want {
		t.Fatalf("object key=%q, want %q", got, want)
	}
}

func TestOwnOSSObjectKeyIgnoresExternalHost(t *testing.T) {
	usecase := NewTaskUsecase(nil, nil, nil, nil, nil, nil, TaskUsecaseOptions{
		OSSPublicHosts: []string{"https://opensource-man.oss-cn-shanghai.aliyuncs.com"},
	})

	if got, ok := usecase.ownOSSObjectKey("https://example.com/cdp-router%2Fq2a%2Fresult.xlsx"); ok {
		t.Fatalf("object key=%q, want external URL ignored", got)
	}
}
