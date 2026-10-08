package gpu

import "testing"

func TestParseDisplays(t *testing.T) {
	body := []byte(`{
	  "SPDisplaysDataType" : [
	    {
	      "_name" : "Apple M4",
	      "sppci_cores" : "10",
	      "sppci_model" : "Apple M4"
	    }
	  ]
	}`)
	got, err := ParseDisplays(body)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Present || got.Name != "Apple M4" || got.Cores != "10" {
		t.Fatalf("device = %+v", got)
	}
}

func TestParseDisplaysIgnoresAPanel(t *testing.T) {
	body := []byte(`{"SPDisplaysDataType":[{"_name":"Color LCD"}]}`)
	got, err := ParseDisplays(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Present {
		t.Fatalf("device = %+v", got)
	}
}

func TestParseDisplaysNumericCores(t *testing.T) {
	body := []byte(`{"SPDisplaysDataType":[{"sppci_model":"Apple M4","sppci_cores":10}]}`)
	got, err := ParseDisplays(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cores != "10" {
		t.Fatalf("cores = %q", got.Cores)
	}
}
