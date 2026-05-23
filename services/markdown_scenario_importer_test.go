package services

import (
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
		t.Fatalf("sections = %d, want 1", len(scenario.Sections))
	}
	section := scenario.Sections[0]
	if !strings.Contains(section.Title, "Suite 1") {
		t.Fatalf("section title = %q, should contain 'Suite 1'", section.Title)
	}
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
	// Verify metadata was parsed
	if tc1.Description == "" {
		t.Fatal("tc1 description (user story) should be parsed from metadata table")
	}
	if !strings.Contains(tc1.Description, "Sebagai Admin") {
		t.Fatalf("tc1 description = %q, should contain user story", tc1.Description)
	}
	// Last step should have the expected result (fewer expected than steps = all on last)
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

func TestBuildScenarioFromMarkdownEmDash(t *testing.T) {
	// Same document but using em dash — instead of regular hyphen -
	content := `# Test Scenario — Master Komponen Global

## 📁 Suite 1: 4.1 Daftar Komponen Global

### 📄 MCG-TC001 — Positive: Akses halaman Daftar Komponen Global sebagai Admin

| Field               | Detail                                                                                                                       |
| ------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| **User Story**      | Sebagai Admin, saya ingin mengakses halaman Daftar Komponen Global                                                           |
| **Category**        | Manual Test                                                                                                                  |
| **Status**          | OPEN                                                                                                                         |
| **Additional Note** | -                                                                                                                            |

**Pre-Condition**

1. User sudah login sebagai Admin

**Test Step**

1. Navigasi ke halaman Daftar Komponen Global (/master/component-global)
2. Periksa halaman yang ditampilkan

**Expected Result**

> Halaman Daftar Komponen Global berhasil dimuat
`

	project := &models.AppProject{ID: "project-1", Name: "QA Project", SpecsRepoID: 1, IssueRepoID: 2}
	scenario, ok := BuildScenarioFromMarkdown("docs/test-scenarios/Master Komponen Global.md", content, project, 7)
	if !ok {
		t.Fatal("expected em-dash scenario to be parsed")
	}
	if len(scenario.Sections) != 1 {
		t.Fatalf("sections = %d, want 1", len(scenario.Sections))
	}
	if len(scenario.Sections[0].TestCases) != 1 {
		t.Fatalf("test cases = %d, want 1", len(scenario.Sections[0].TestCases))
	}
	tc := scenario.Sections[0].TestCases[0]
	if tc.Code != "MCG-TC001" {
		t.Fatalf("code = %q, want MCG-TC001", tc.Code)
	}
	if tc.Type != "positive" {
		t.Fatalf("type = %q, want positive", tc.Type)
	}
}

func TestBuildScenarioFromMarkdownEnDash(t *testing.T) {
	// Same document but using en dash –
	content := `# Test Scenario — Master Komponen Global

## 📁 Suite 1: 4.1 Daftar Komponen Global

### 📄 MCG-TC001 – Positive: Akses halaman Daftar Komponen Global sebagai Admin

| Field               | Detail                                                                                                                       |
| ------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| **User Story**      | Sebagai Admin, saya ingin mengakses halaman Daftar Komponen Global                                                           |
| **Category**        | Manual Test                                                                                                                  |
| **Status**          | OPEN                                                                                                                         |
| **Additional Note** | -                                                                                                                            |

**Pre-Condition**

1. User sudah login sebagai Admin

**Test Step**

1. Navigasi ke halaman Daftar Komponen Global (/master/component-global)
2. Periksa halaman yang ditampilkan

**Expected Result**

> Halaman Daftar Komponen Global berhasil dimuat
`

	project := &models.AppProject{ID: "project-1", Name: "QA Project", SpecsRepoID: 1, IssueRepoID: 2}
	scenario, ok := BuildScenarioFromMarkdown("docs/test-scenarios/Master Komponen Global.md", content, project, 7)
	if !ok {
		t.Fatal("expected en-dash scenario to be parsed")
	}
	if len(scenario.Sections) != 1 {
		t.Fatalf("sections = %d, want 1", len(scenario.Sections))
	}
	if len(scenario.Sections[0].TestCases) != 1 {
		t.Fatalf("test cases = %d, want 1", len(scenario.Sections[0].TestCases))
	}
	tc := scenario.Sections[0].TestCases[0]
	if tc.Code != "MCG-TC001" {
		t.Fatalf("code = %q, want MCG-TC001", tc.Code)
	}
}

func TestBuildScenarioFromMarkdownMultipleSuites(t *testing.T) {
	content := `# Master Komponen Global

## 📁 Suite 1: 4.1 Daftar Komponen Global

### 📄 MCG-TC001 - Positive: Akses halaman sebagai Admin

| Field          | Detail                     |
| -------------- | -------------------------- |
| **User Story** | Sebagai Admin, melihat daftar |

**Pre-Condition**

1. Login sebagai Admin

**Test Step**

1. Buka halaman daftar
2. Periksa tampilan

**Expected Result**

> Daftar berhasil dimuat

### 📄 MCG-TC002 - Negative: Akses sebagai non-Admin

| Field          | Detail                     |
| -------------- | -------------------------- |
| **User Story** | non-Admin tidak bisa akses |
| **Status**     | OPEN                       |

**Pre-Condition**

1. Login sebagai non-Admin

**Test Step**

1. Buka halaman daftar

**Expected Result**

> Error unauthorized

## 📁 Suite 2: 4.2 Tambah Komponen Global

### 📄 MCG-TC015 - Positive: Tambah komponen berhasil

| Field          | Detail                                 |
| -------------- | -------------------------------------- |
| **User Story** | Sebagai Admin, saya ingin tambah komponen |

**Pre-Condition**

1. Login sebagai Admin

**Test Step**

1. Isi form
2. Klik Simpan

**Expected Result**

> Komponen berhasil disimpan

## 📁 Suite 3: 4.3 Edit Komponen Global

### 📄 MCG-TC022 - Positive: Edit komponen berhasil

| Field          | Detail                               |
| -------------- | ------------------------------------ |
| **User Story** | Sebagai Admin, saya ingin edit komponen |
| **Status**     | READY                                |

**Pre-Condition**

1. Login sebagai Admin

**Test Step**

1. Buka halaman edit
2. Ubah data
3. Klik Simpan

**Expected Result**

> Edit berhasil
`

	project := &models.AppProject{ID: "project-1", Name: "QA Project", SpecsRepoID: 1, IssueRepoID: 2}
	scenario, ok := BuildScenarioFromMarkdown("docs/test-scenarios/Master Komponen Global.md", content, project, 7)
	if !ok {
		t.Fatal("expected multi-suite scenario to be parsed")
	}
	if len(scenario.Sections) != 3 {
		t.Fatalf("sections = %d, want 3", len(scenario.Sections))
	}

	// Suite 1: 2 test cases
	s1 := scenario.Sections[0]
	if !strings.Contains(s1.Title, "Suite 1") {
		t.Fatalf("section 0 title = %q, should contain Suite 1", s1.Title)
	}
	if len(s1.TestCases) != 2 {
		t.Fatalf("section 0 test cases = %d, want 2", len(s1.TestCases))
	}
	if s1.TestCases[0].Code != "MCG-TC001" {
		t.Fatalf("tc1 code = %q", s1.TestCases[0].Code)
	}
	if s1.TestCases[1].Code != "MCG-TC002" {
		t.Fatalf("tc2 code = %q", s1.TestCases[1].Code)
	}

	// Suite 2: 1 test case
	s2 := scenario.Sections[1]
	if !strings.Contains(s2.Title, "Suite 2") {
		t.Fatalf("section 1 title = %q, should contain Suite 2", s2.Title)
	}
	if len(s2.TestCases) != 1 {
		t.Fatalf("section 1 test cases = %d, want 1", len(s2.TestCases))
	}
	if s2.TestCases[0].Code != "MCG-TC015" {
		t.Fatalf("tc code = %q, want MCG-TC015", s2.TestCases[0].Code)
	}

	// Suite 3: 1 test case
	s3 := scenario.Sections[2]
	if !strings.Contains(s3.Title, "Suite 3") {
		t.Fatalf("section 2 title = %q, should contain Suite 3", s3.Title)
	}
	if len(s3.TestCases) != 1 {
		t.Fatalf("section 2 test cases = %d, want 1", len(s3.TestCases))
	}
	if s3.TestCases[0].Code != "MCG-TC022" {
		t.Fatalf("tc code = %q, want MCG-TC022", s3.TestCases[0].Code)
	}

	// Verify metadata was parsed
	tc1 := s1.TestCases[0]
	if tc1.Description == "" || !strings.Contains(tc1.Description, "melihat daftar") {
		t.Fatalf("tc1 description = %q", tc1.Description)
	}

	// Verify metadata note cleanup
	tc2 := s1.TestCases[1]
	if tc2.Note != "" {
		t.Fatalf("tc2 should have empty note (was '-'), got %q", tc2.Note)
	}
}

func TestBuildScenarioFromMarkdownNumberedExpectedResults(t *testing.T) {
	content := `# Test Scenario

## 📁 Suite 1: Some Suite

### 📄 TC-001 - Positive: Numbered expected results

| Field          | Detail                     |
| -------------- | -------------------------- |
| **User Story** | Test numbered expected     |

**Pre-Condition**

1. Precondition

**Test Step**

1. Step one
2. Step two
3. Step three

**Expected Result**

> 1. Result for step one
> 2. Result for step two
> 3. Result for step three
`

	project := &models.AppProject{ID: "project-1", Name: "QA Project", SpecsRepoID: 1, IssueRepoID: 2}
	scenario, ok := BuildScenarioFromMarkdown("docs/test-scenarios/test.md", content, project, 7)
	if !ok {
		t.Fatal("expected scenario to be parsed")
	}
	if len(scenario.Sections) != 1 {
		t.Fatalf("sections = %d", len(scenario.Sections))
	}
	tc := scenario.Sections[0].TestCases[0]
	if len(tc.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(tc.Steps))
	}
	if tc.Steps[0].Expected != "Result for step one" {
		t.Fatalf("step 0 expected = %q", tc.Steps[0].Expected)
	}
	if tc.Steps[1].Expected != "Result for step two" {
		t.Fatalf("step 1 expected = %q", tc.Steps[1].Expected)
	}
	if tc.Steps[2].Expected != "Result for step three" {
		t.Fatalf("step 2 expected = %q", tc.Steps[2].Expected)
	}
}

func TestBuildScenarioFromMarkdownMoreExpectedThanSteps(t *testing.T) {
	content := `# Test Scenario

## 📁 Suite 1: Some Suite

### 📄 TC-001 - Positive: More expected than steps

| Field          | Detail                 |
| -------------- | ---------------------- |
| **User Story** | More expected than actions |

**Pre-Condition**

1. Precondition

**Test Step**

1. Step one
2. Step two

**Expected Result**

> 1. Result for step one
> 2. Extra result A
> 3. Extra result B
`

	project := &models.AppProject{ID: "project-1", Name: "QA Project", SpecsRepoID: 1, IssueRepoID: 2}
	scenario, ok := BuildScenarioFromMarkdown("docs/test-scenarios/test.md", content, project, 7)
	if !ok {
		t.Fatal("expected scenario to be parsed")
	}
	tc := scenario.Sections[0].TestCases[0]
	if len(tc.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(tc.Steps))
	}
	if tc.Steps[0].Expected != "Result for step one" {
		t.Fatalf("step 0 expected = %q", tc.Steps[0].Expected)
	}
	if !strings.Contains(tc.Steps[1].Expected, "Extra result A") {
		t.Fatalf("step 1 expected should contain extra results, got: %q", tc.Steps[1].Expected)
	}
}
