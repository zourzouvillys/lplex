package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "lplexdump.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig_GlobalExcludePGN(t *testing.T) {
	path := writeConfig(t, `
exclude-pgn = [60928, 126996]

boats {
  test {
    mdns = "test1"
  }
}
`)
	dc, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{60928, 126996}
	if len(dc.ExcludePGNs) != len(want) {
		t.Fatalf("global ExcludePGNs = %v, want %v", dc.ExcludePGNs, want)
	}
	for i, v := range want {
		if dc.ExcludePGNs[i] != v {
			t.Errorf("global ExcludePGNs[%d] = %d, want %d", i, dc.ExcludePGNs[i], v)
		}
	}
}

func TestLoadConfig_PerBoatExcludePGN(t *testing.T) {
	path := writeConfig(t, `
boats {
  sv-dockwise {
    mdns = "inuc1"
    exclude-pgn = [129025, 129026]
  }
  test-bench {
    mdns = "testpi"
  }
}
`)
	dc, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	// sv-dockwise should have its exclusions.
	bc := dc.Boats["sv-dockwise"]
	want := []uint32{129025, 129026}
	if len(bc.ExcludePGNs) != len(want) {
		t.Fatalf("sv-dockwise ExcludePGNs = %v, want %v", bc.ExcludePGNs, want)
	}
	for i, v := range want {
		if bc.ExcludePGNs[i] != v {
			t.Errorf("sv-dockwise ExcludePGNs[%d] = %d, want %d", i, bc.ExcludePGNs[i], v)
		}
	}

	// test-bench should have none.
	if len(dc.Boats["test-bench"].ExcludePGNs) != 0 {
		t.Errorf("test-bench ExcludePGNs = %v, want empty", dc.Boats["test-bench"].ExcludePGNs)
	}
}

func TestLoadConfig_GlobalAndPerBoatExcludePGN(t *testing.T) {
	path := writeConfig(t, `
exclude-pgn = [60928]

boats {
  sv-dockwise {
    mdns = "inuc1"
    exclude-pgn = [129025]
  }
}
`)
	dc, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(dc.ExcludePGNs) != 1 || dc.ExcludePGNs[0] != 60928 {
		t.Errorf("global ExcludePGNs = %v, want [60928]", dc.ExcludePGNs)
	}

	bc := dc.Boats["sv-dockwise"]
	if len(bc.ExcludePGNs) != 1 || bc.ExcludePGNs[0] != 129025 {
		t.Errorf("sv-dockwise ExcludePGNs = %v, want [129025]", bc.ExcludePGNs)
	}
}

func TestLoadConfig_SingleExcludePGN(t *testing.T) {
	path := writeConfig(t, `
exclude-pgn = 60928

boats {
  test {
    mdns = "test1"
    exclude-pgn = 129025
  }
}
`)
	dc, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(dc.ExcludePGNs) != 1 || dc.ExcludePGNs[0] != 60928 {
		t.Errorf("global ExcludePGNs = %v, want [60928]", dc.ExcludePGNs)
	}
	if len(dc.Boats["test"].ExcludePGNs) != 1 || dc.Boats["test"].ExcludePGNs[0] != 129025 {
		t.Errorf("test ExcludePGNs = %v, want [129025]", dc.Boats["test"].ExcludePGNs)
	}
}

func TestLoadConfig_NoExcludePGN(t *testing.T) {
	path := writeConfig(t, `
boats {
  test {
    mdns = "test1"
  }
}
`)
	dc, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if dc.ExcludePGNs != nil {
		t.Errorf("global ExcludePGNs = %v, want nil", dc.ExcludePGNs)
	}
	if dc.Boats["test"].ExcludePGNs != nil {
		t.Errorf("test ExcludePGNs = %v, want nil", dc.Boats["test"].ExcludePGNs)
	}
}

func TestLoadConfig_InvalidExcludePGN(t *testing.T) {
	path := writeConfig(t, `
exclude-pgn = "not-a-number"

boats {
  test {
    mdns = "test1"
  }
}
`)
	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid global exclude-pgn")
	}
}

func TestLoadConfig_InvalidBoatExcludePGN(t *testing.T) {
	path := writeConfig(t, `
boats {
  test {
    mdns = "test1"
    exclude-pgn = ["abc"]
  }
}
`)
	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid boat exclude-pgn")
	}
}

func TestLoadConfig_GlobalExcludeName(t *testing.T) {
	path := writeConfig(t, `
exclude-name = ["00A1B2C3D4E5F600", "00DEADBEEFCAFE00"]

boats {
  test {
    mdns = "test1"
  }
}
`)
	dc, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"00A1B2C3D4E5F600", "00DEADBEEFCAFE00"}
	if len(dc.ExcludeNames) != len(want) {
		t.Fatalf("global ExcludeNames = %v, want %v", dc.ExcludeNames, want)
	}
	for i, v := range want {
		if dc.ExcludeNames[i] != v {
			t.Errorf("global ExcludeNames[%d] = %q, want %q", i, dc.ExcludeNames[i], v)
		}
	}
}

func TestLoadConfig_PerBoatExcludeName(t *testing.T) {
	path := writeConfig(t, `
boats {
  sv-dockwise {
    mdns = "inuc1"
    exclude-name = ["00A1B2C3D4E5F600"]
  }
  test-bench {
    mdns = "testpi"
  }
}
`)
	dc, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	bc := dc.Boats["sv-dockwise"]
	if len(bc.ExcludeNames) != 1 || bc.ExcludeNames[0] != "00A1B2C3D4E5F600" {
		t.Errorf("sv-dockwise ExcludeNames = %v, want [00A1B2C3D4E5F600]", bc.ExcludeNames)
	}
	if dc.Boats["test-bench"].ExcludeNames != nil {
		t.Errorf("test-bench ExcludeNames = %v, want nil", dc.Boats["test-bench"].ExcludeNames)
	}
}

func TestLoadConfig_GlobalAndPerBoatExcludeName(t *testing.T) {
	path := writeConfig(t, `
exclude-name = ["00DEADBEEFCAFE00"]

boats {
  sv-dockwise {
    mdns = "inuc1"
    exclude-name = ["00A1B2C3D4E5F600"]
  }
}
`)
	dc, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(dc.ExcludeNames) != 1 || dc.ExcludeNames[0] != "00DEADBEEFCAFE00" {
		t.Errorf("global ExcludeNames = %v, want [00DEADBEEFCAFE00]", dc.ExcludeNames)
	}
	bc := dc.Boats["sv-dockwise"]
	if len(bc.ExcludeNames) != 1 || bc.ExcludeNames[0] != "00A1B2C3D4E5F600" {
		t.Errorf("sv-dockwise ExcludeNames = %v, want [00A1B2C3D4E5F600]", bc.ExcludeNames)
	}
}

func TestLoadConfig_SingleExcludeName(t *testing.T) {
	path := writeConfig(t, `
exclude-name = "00DEADBEEFCAFE00"

boats {
  test {
    mdns = "test1"
    exclude-name = "00A1B2C3D4E5F600"
  }
}
`)
	dc, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(dc.ExcludeNames) != 1 || dc.ExcludeNames[0] != "00DEADBEEFCAFE00" {
		t.Errorf("global ExcludeNames = %v, want [00DEADBEEFCAFE00]", dc.ExcludeNames)
	}
	if len(dc.Boats["test"].ExcludeNames) != 1 || dc.Boats["test"].ExcludeNames[0] != "00A1B2C3D4E5F600" {
		t.Errorf("test ExcludeNames = %v, want [00A1B2C3D4E5F600]", dc.Boats["test"].ExcludeNames)
	}
}
