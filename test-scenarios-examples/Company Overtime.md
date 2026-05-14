# Test Scenarios: Company Overtime

## Preconditions

- User yang terautentikasi memiliki JWT token yang valid
- User yang terautentikasi memiliki permission yang sesuai (CreateCompanyOvertime, ReadCompanyOvertime, UpdateCompanyOvertime, DeleteCompanyOvertime sesuai kebutuhan)
- Minimal satu record company ada dengan company code yang valid
- Minimal satu record overtime type ada (disinkronkan dari PI-Smart)
- Record company overtime uji ada untuk skenario list/detail/update/delete

## Scenarios

### Scenario 1: Daftar Aturan Company Overtime dengan Pagination

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:read` dengan cakupan company "PI01" | Autentikasi berhasil |
| 2 | Kirim `GET /api/v1/company-overtimes?page=1&limit=10` | Request diproses |
| 3 | Sistem memfilter berdasarkan cakupan company | Hanya aturan untuk "PI01" yang disertakan |
| 4 | Sistem mengembalikan daftar dengan pagination | 200 OK dengan daftar dan metadata pagination |
| 5 | Verifikasi setiap item berisi company_code, overtime_type, max_hour, jumlah items | Semua field ada |

### Scenario 2: Mendapatkan Detail Company Overtime

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:read` | Autentikasi berhasil |
| 2 | Kirim `GET /api/v1/company-overtimes/:id` dengan ID yang valid | Request diproses |
| 3 | Sistem mengembalikan detail lengkap dengan items | 200 OK dengan semua items termasuk criteria, fix_rate/formula, is_progressive |

### Scenario 3: Membuat Company Overtime dengan Item FixRate

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-overtimes` dengan company_code="PI01", overtime_type="WEEKDAY", max_hour=4.0, items=[{criteria: {"JobGrade": ["G1", "G2"]}, fix_rate: 50000, is_progressive: false}] | Request diproses |
| 3 | Sistem memvalidasi payload | Semua validasi berhasil |
| 4 | Sistem membuat aturan dengan items | 201 Created dengan detail aturan lengkap |
| 5 | Kirim `GET /api/v1/company-overtimes/:id` untuk memverifikasi | Items ada dengan fix_rate=50000 |

### Scenario 4: Membuat Company Overtime dengan Item Formula

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-overtimes` dengan items=[{criteria: {"JobGrade": ["G3"]}, formula: "gaji_pokok / 173", is_progressive: true}] | Request diproses |
| 3 | Sistem memvalidasi item berbasis formula | Validasi berhasil |
| 4 | Sistem membuat aturan | 201 Created dengan item formula dan is_progressive=true |

### Scenario 5: Membuat Company Overtime dengan FixRate dan Formula Sekaligus

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-overtimes` dengan items=[{criteria: {"JobGrade": ["G1"]}, fix_rate: 50000, formula: "gaji_pokok / 173", is_progressive: false}] | Request diproses |
| 3 | Sistem mendeteksi pelanggaran mutual exclusivity | fix_rate DAN formula keduanya ada |
| 4 | Sistem mengembalikan response error | 400 Bad Request yang menunjukkan fix_rate dan formula saling eksklusif |

### Scenario 6: Membuat Company Overtime Tanpa FixRate maupun Formula

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-overtimes` dengan items=[{criteria: {"JobGrade": ["G1"]}, is_progressive: false}] | Request diproses |
| 3 | Sistem mendeteksi konfigurasi rate yang hilang | Tidak ada fix_rate maupun formula |
| 4 | Sistem mengembalikan response error | 400 Bad Request yang menunjukkan salah satu fix_rate atau formula harus disediakan |

### Scenario 7: Membuat Company Overtime dengan Criteria Kosong

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-overtimes` dengan items=[{criteria: {}, fix_rate: 50000, is_progressive: false}] | Request diproses |
| 3 | Sistem memvalidasi criteria | Criteria kosong |
| 4 | Sistem mengembalikan response error | 400 Bad Request yang menunjukkan criteria harus berisi minimal satu kondisi |

### Scenario 8: Membuat Company Overtime dengan Max Hour Nol

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-overtimes` dengan max_hour=0 dan field lain yang valid | Request diproses |
| 3 | Sistem memvalidasi max_hour | max_hour <= 0 |
| 4 | Sistem mengembalikan response error | 400 Bad Request yang menunjukkan max hour harus lebih dari 0 |

### Scenario 9: Membuat Company Overtime dengan Array Items Kosong

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-overtimes` dengan items=[] | Request diproses |
| 3 | Sistem memvalidasi array items | Array kosong |
| 4 | Sistem mengembalikan response error | 400 Bad Request yang menunjukkan minimal satu item wajib ada |

### Scenario 10: Memperbarui Aturan Company Overtime

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:update` | Autentikasi berhasil |
| 2 | Kirim `PATCH /api/v1/company-overtimes/:id` dengan max_hour=6.0 dan items yang diperbarui | Request diproses |
| 3 | Sistem memperbarui aturan dan items | 200 OK dengan record yang diperbarui |

### Scenario 11: Menghapus Aturan Company Overtime

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:delete` | Autentikasi berhasil |
| 2 | Kirim `DELETE /api/v1/company-overtimes/:id` | Request diproses |
| 3 | Sistem menghapus aturan dan semua items terkait | 200 OK dengan pesan sukses |
| 4 | Kirim `GET /api/v1/company-overtimes/:id` untuk memverifikasi | 404 Not Found |

### Scenario 12: Ekspor Company Overtime ke PDF

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:read` | Autentikasi berhasil |
| 2 | Kirim `GET /api/v1/company-overtimes/export?export_type=pdf` | Request diproses |
| 3 | Sistem menghasilkan file PDF | 200 OK dengan unduhan file PDF |
| 4 | Verifikasi file berisi kolom company_code, overtime_type, max_hour | Semua kolom yang diharapkan ada |

### Scenario 13: Membuat dengan Item Rate Progresif

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-overtime:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-overtimes` dengan items berisi is_progressive=true dan formula | Request diproses |
| 3 | Sistem menyimpan flag progressive | Aturan dibuat |
| 4 | Verifikasi item memiliki is_progressive=true | Flag progressive disimpan dengan benar |
