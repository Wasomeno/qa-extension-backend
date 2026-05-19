package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qa-extension-backend/internal/models"
)

func TestBuildScenarioFromMarkdownNewFormat(t *testing.T) {
	content := `# Test Scenario — Master Komponen Global

> Master Komponen Global digunakan untuk mengelola daftar komponen payroll global.

## 📁 Suite 1: 4.1 Daftar Komponen Global

### 📄 MCG-TC001 - Positive: Akses halaman Daftar Komponen Global sebagai Admin

| Field               | Detail                                                                                                                       |
| ------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| **User Story**      | Sebagai Admin, saya ingin mengakses halaman Daftar Komponen Global agar dapat melihat seluruh daftar komponen payroll global |
| **Category**        | Manual Test                                                                                                                  |
| **Status**          | OPEN                                                                                                                         |
| **Additional Note** | -                                                                                                                            |

**Pre-Condition**

1. User sudah login sebagai Admin
2. Data Komponen Global tersedia di sistem

**Test Step**

1. Navigasi ke halaman Daftar Komponen Global (/master/component-global)
2. Periksa halaman yang ditampilkan

**Expected Result**

> Halaman Daftar Komponen Global berhasil dimuat Tabel daftar komponen global tampil dengan data yang tersedia

### 📄 MCG-TC002 - Negative: Akses halaman Daftar Komponen Global sebagai non-Admin

| Field               | Detail                                                                                                           |
| ------------------- | ---------------------------------------------------------------------------------------------------------------- |
| **User Story**      | Sebagai user tanpa role Admin, saya tidak dapat mengakses halaman Daftar Komponen Global agar data tetap terjaga |
| **Category**        | Manual Test                                                                                                      |
| **Status**          | OPEN                                                                                                             |
| **Additional Note** | -                                                                                                                |

**Pre-Condition**

1. User sudah login dengan role selain Admin

**Test Step**

1. Navigasi ke halaman Daftar Komponen Global (/master/component-global)
2. Periksa respons sistem

**Expected Result**

> User tidak dapat mengakses halaman Sistem menampilkan halaman error atau redirect ke halaman yang sesuai (unauthorized/forbidden)
`

	project := &models.AppProject{ID: "project-1", Name: "QA Project", SpecsRepoID: 1, IssueRepoID: 2}
	scenario, ok := BuildScenarioFromMarkdown("docs/test-scenarios/Master Komponen Global.md", content, project, 7)
	if !ok {
		t.Fatal("expected new-format scenario to be parsed")
	}
	if len(scenario.Sections) != 1 {
		t.Fatalf("sections = %d", len(scenario.Sections))
	}
	section := scenario.Sections[0]
	if len(section.TestCases) != 2 {
		t.Fatalf("test cases = %d, want 2", len(section.TestCases))
	}

	tc1 := section.TestCases[0]
	if tc1.Code != "MCG-TC001" {
		t.Fatalf("tc1 code = %q, want MCG-TC001", tc1.Code)
	}
	if !strings.Contains(tc1.Title, "Positive") || !strings.Contains(tc1.Title, "Akses halaman") {
		t.Fatalf("tc1 title = %q", tc1.Title)
	}
	if len(tc1.Steps) != 2 {
		t.Fatalf("tc1 steps = %d, want 2", len(tc1.Steps))
	}
	if !strings.Contains(tc1.PreCondition, "Admin") {
		t.Fatalf("tc1 preCondition should mention Admin, got: %q", tc1.PreCondition)
	}
	// Last step should have the expected result
	lastStep := tc1.Steps[len(tc1.Steps)-1]
	if !strings.Contains(lastStep.Expected, "berhasil dimuat") {
		t.Fatalf("last step expected should contain expected result, got: %q", lastStep.Expected)
	}

	tc2 := section.TestCases[1]
	if tc2.Code != "MCG-TC002" {
		t.Fatalf("tc2 code = %q, want MCG-TC002", tc2.Code)
	}
	if !strings.Contains(tc2.Title, "Negative") {
		t.Fatalf("tc2 title should contain Negative, got: %q", tc2.Title)
	}
	if tc2.Priority != models.PriorityMedium {
		t.Fatalf("tc2 priority = %q, want medium", tc2.Priority)
	}
	if tc2.Type != "negative" {
		t.Fatalf("tc2 type = %q, want negative", tc2.Type)
	}
}

func TestBuildScenarioFromMarkdownExample(t *testing.T) {
	path := filepath.Join("..", "test-scenarios-examples", "Company Management FlowG.md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read example: %v", err)
	}

	project := &models.AppProject{ID: "project-1", Name: "QA Project", SpecsRepoID: 1, IssueRepoID: 2}
	scenario, ok := BuildScenarioFromMarkdown("docs/test-scenarios/Company Management FlowG.md", string(content), project, 7)
	if !ok {
		t.Fatal("expected scenario to be parsed")
	}
	if scenario.Title != "Test Scenarios: Company Management" {
		t.Fatalf("title = %q", scenario.Title)
	}
	if len(scenario.Sections) != 1 {
		t.Fatalf("sections = %d", len(scenario.Sections))
	}
	if got := len(scenario.Sections[0].TestCases); got != 8 {
		t.Fatalf("test cases = %d", got)
	}
	first := scenario.Sections[0].TestCases[0]
	if first.Title != "Daftar Company dengan Pagination" {
		t.Fatalf("first title = %q", first.Title)
	}
	if got := len(first.Steps); got != 4 {
		t.Fatalf("first steps = %d", got)
	}
	if first.PreCondition == "" {
		t.Fatal("expected preconditions")
	}
	if first.AutomationType != nil {
		t.Fatalf("initial automation type = %q, want nil", *first.AutomationType)
	}
	data, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first test case: %v", err)
	}
	if !strings.Contains(string(data), `"automationType":null`) {
		t.Fatalf("initial automationType should be null in JSON, got %s", data)
	}
}
