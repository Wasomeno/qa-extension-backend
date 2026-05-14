# Test Scenarios: Company Payroll State

## Preconditions

- User yang terautentikasi memiliki JWT token yang valid dengan permission payroll yang sesuai
- Minimal satu company ada di sistem (bukan company_code "9999")
- Database berisi periode payroll sebelumnya dalam berbagai status untuk pengujian transisi
- Redis tersedia untuk pelacakan progres
- Periode overtime terkait ada untuk validasi IsRelatedOvertimeClosed()

## Scenarios

### Scenario 1: Membuat Payroll State Oncycle untuk Periode Baru

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Pastikan periode oncycle sebelumnya untuk semua company berstatus "closed" | Prasyarat terpenuhi |
| 2 | Kirim request pembuatan payroll state dengan CompanyCode, Period "2025-03", dan PayrollType "oncycle" yang valid | Request diproses |
| 3 | Sistem memvalidasi tidak ada duplikat (company, type, period) | Tidak ada duplikat ditemukan |
| 4 | Sistem membuat payroll state oncycle untuk SEMUA company di sistem untuk periode "2025-03" | Semua company payroll state dibuat dengan status "created" |
| 5 | Verifikasi setiap company memiliki record payroll state baru | Semua record ada dengan status "created" dan type "oncycle" |

### Scenario 2: Menolak Pembuatan Payroll State dengan Company Code 9999 yang Dicadangkan

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Kirim request pembuatan payroll state dengan CompanyCode "9999", Period "2025-03", PayrollType "oncycle" | Request diproses |
| 2 | Sistem memvalidasi company_code | Validasi gagal untuk kode cadangan "9999" |
| 3 | Sistem mengembalikan response error | 400 Bad Request yang menunjukkan company_code "9999" tidak dapat digunakan |

### Scenario 3: Menolak Pembuatan Payroll State Duplikat

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Pastikan payroll state ada untuk (CompanyCode "1000", PayrollType "oncycle", Period "2025-01") | Prasyarat terpenuhi |
| 2 | Kirim request pembuatan dengan CompanyCode "1000", PayrollType "oncycle", Period "2025-01" yang sama | Request diproses |
| 3 | Sistem mendeteksi kombinasi duplikat | Konflik terdeteksi |
| 4 | Sistem mengembalikan response error | 409 Conflict error yang menunjukkan payroll state sudah ada |

### Scenario 4: Menolak Pembuatan Oncycle Ketika Periode Sebelumnya Belum Ditutup

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Pastikan periode oncycle sebelumnya "2025-02" berstatus "open" (belum closed) | Prasyarat terpenuhi |
| 2 | Kirim request pembuatan untuk periode oncycle "2025-03" | Request diproses |
| 3 | Sistem memeriksa status periode sebelumnya | Periode sebelumnya belum "closed" |
| 4 | Sistem mengembalikan response error | Error yang menunjukkan periode sebelumnya harus ditutup sebelum membuat yang baru |

### Scenario 5: Siklus Hidup Transisi State Lengkap (created ke closed)

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Buat payroll state; verifikasi berstatus "created" | Status adalah "created" |
| 2 | Transisi payroll state ke "open" | Status berubah menjadi "open" |
| 3 | Transisi payroll state ke "lock" | Status berubah menjadi "lock" |
| 4 | Transisi payroll state kembali ke "open" (reversible) | Status berubah kembali menjadi "open" |
| 5 | Transisi payroll state ke "lock" lagi | Status berubah menjadi "lock" |
| 6 | Picu pemrosesan payroll (dengan overtime terkait sudah closed) | Status bertransisi ke "processing", kemudian ke "closed" setelah selesai |

### Scenario 6: Menolak Pemrosesan Ketika Overtime Terkait Belum Ditutup

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Pastikan payroll state berstatus "lock" | Prasyarat terpenuhi |
| 2 | Pastikan periode overtime terkait TIDAK ditutup (IsRelatedOvertimeClosed mengembalikan false) | Prasyarat terpenuhi |
| 3 | Picu pemrosesan payroll | Request diproses |
| 4 | Sistem memeriksa IsRelatedOvertimeClosed() | Mengembalikan false |
| 5 | Sistem mengembalikan response error | Error yang menunjukkan periode overtime terkait harus ditutup terlebih dahulu |

### Scenario 7: Membuat State Non-Payroll-Taxable Tanpa Flag is_periodic

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Kirim request pembuatan dengan PayrollType "non-payroll-taxable" dan tanpa flag is_periodic | Request diproses |
| 2 | Sistem memvalidasi field wajib untuk tipe non-payroll-taxable | Flag is_periodic tidak ada |
| 3 | Sistem mengembalikan response error | 400 Bad Request yang menunjukkan is_periodic wajib untuk tipe non-payroll-taxable |

### Scenario 8: Membuat Non-Taxable dari Oncycle

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Pastikan payroll state oncycle yang selesai ada untuk periode "2025-01" | Prasyarat terpenuhi |
| 2 | Picu "create non-taxable from oncycle" untuk periode tersebut | Request diproses |
| 3 | Sistem membaca data payroll oncycle dan menghasilkan record non-taxable | Record non-payroll-taxable otomatis dihasilkan |
| 4 | Verifikasi payroll state non-taxable dan record employee terkait ada | Record dibuat dengan type dan period yang benar |

### Scenario 9: Melacak Progres Pemrosesan

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Picu pemrosesan payroll pada payroll state yang terkunci dengan overtime terkait sudah closed | Pemrosesan dimulai, status bertransisi ke "processing" |
| 2 | Query progres pemrosesan saat job sedang berjalan | Sistem mengembalikan data progres berdasarkan jumlah employee payroll (misalnya 50/200 employee diproses) |
| 3 | Tunggu pemrosesan selesai | Status bertransisi ke "closed" |
| 4 | Query progres lagi | Progres menunjukkan 100% selesai |

### Scenario 10: Menolak Transisi State Tidak Valid

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Pastikan payroll state berstatus "created" | Prasyarat terpenuhi |
| 2 | Coba transisi langsung ke "lock" (melewati "open") | Request diproses |
| 3 | Sistem memvalidasi transisi state | Transisi tidak valid terdeteksi |
| 4 | Sistem mengembalikan response error | Error yang menunjukkan transisi dari "created" ke "lock" tidak diperbolehkan |
