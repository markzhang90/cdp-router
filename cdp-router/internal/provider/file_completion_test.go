package provider

import "testing"

func TestNewFileCompletionUsesProviderFilesForQ2AProviders(t *testing.T) {
	for _, providerCode := range []string{"mpzdata", " liuandata "} {
		if _, ok := newFileCompletion(providerCode).(mpzdataFileCompletion); !ok {
			t.Fatalf("newFileCompletion(%q) should use provider files completion", providerCode)
		}
	}
}

func TestNewFileCompletionUsesUploadedFilesByDefault(t *testing.T) {
	if _, ok := newFileCompletion("vendor_a").(uploadedFileCompletion); !ok {
		t.Fatal("newFileCompletion should use uploaded files completion by default")
	}
}
