# Test Scenarios: Company Component

## Preconditions

- User yang terautentikasi memiliki JWT token yang valid
- User yang terautentikasi memiliki permission yang sesuai (CreateCompanyComponent, ReadCompanyComponent, UpdateCompanyComponent sesuai kebutuhan)
- Minimal satu record company ada dengan company code yang valid
- Minimal satu record master component ada untuk dihubungkan
- Record income tax category uji ada
- Record company component uji ada untuk skenario list/detail/update

## Scenarios

### Scenario 1: Daftar Company Component dengan Pagination

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-component:read` dengan cakupan company "PI01" | Autentikasi berhasil |
| 2 | Kirim `GET /api/v1/company-components?page=1&limit=10` | Request diproses |
| 3 | Sistem memfilter company component berdasarkan cakupan company user | Hanya company component untuk "PI01" yang disertakan |
| 4 | Sistem mengembalikan daftar dengan pagination | 200 OK dengan daftar company component dan metadata pagination |
| 5 | Verifikasi setiap item berisi code, company_code, component_code, calculation_method, tax_type | Semua field ada |

### Scenario 2: Membuat Company Component dengan Metode Formula

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-component:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-components` dengan calculation_method="formula", formula="gaji_pokok * 0.1", company_code="PI01", component_code="TM001", tax_type="tax" | Request diproses |
| 3 | Sistem memvalidasi calculation_method dan field formula yang wajib | Validasi berhasil |
| 4 | Sistem membuat company component | 201 Created dengan detail company component lengkap |
| 5 | Kirim `GET /api/v1/company-components/:id` untuk memverifikasi | Field formula berisi "gaji_pokok * 0.1" |

### Scenario 3: Membuat Company Component dengan Metode Table

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-component:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-components` dengan calculation_method="table", criteria={"ranges": [{"min": 0, "max": 5000000, "value": 500000}]}, company_code="PI01", component_code="TM002" | Request diproses |
| 3 | Sistem memvalidasi calculation_method dan field criteria yang wajib | Validasi berhasil |
| 4 | Sistem membuat company component | 201 Created dengan criteria disimpan sebagai JSONB |

### Scenario 4: Membuat Company Component dengan Metode Nominal

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-component:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-components` dengan calculation_method="nominal", nominal=1500000.00, company_code="PI01", component_code="TM003" | Request diproses |
| 3 | Sistem memvalidasi calculation_method dan field nominal yang wajib | Validasi berhasil |
| 4 | Sistem membuat company component | 201 Created dengan nilai nominal 1500000.00 |

### Scenario 5: Membuat Company Component dengan Min/Max Tidak Valid

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-component:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-components` dengan min=5000000, max=1000000, dan field lain yang valid | Request diproses |
| 3 | Sistem memvalidasi constraint Min <= Max | Validasi gagal: Min > Max |
| 4 | Sistem mengembalikan response error | 400 Bad Request dengan pesan "Min must be less than or equal to Max" |

### Scenario 6: Membuat Company Component dengan Periode Validitas Tidak Valid

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-component:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-components` dengan valid_start_at="2026-06-01", valid_end_at="2026-01-01", dan field lain yang valid | Request diproses |
| 3 | Sistem memvalidasi ValidEndAt >= ValidStartAt | Validasi gagal: tanggal akhir sebelum tanggal mulai |
| 4 | Sistem mengembalikan response error | 400 Bad Request dengan pesan "ValidEndAt must be greater than or equal to ValidStartAt" |

### Scenario 7: Membuat Company Component Tanpa Field Wajib untuk Metode

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-component:create` | Autentikasi berhasil |
| 2 | Kirim `POST /api/v1/company-components` dengan calculation_method="formula" tetapi tanpa field formula | Request diproses |
| 3 | Sistem memvalidasi field wajib untuk metode formula | Field formula tidak ada |
| 4 | Sistem mengembalikan response error | 400 Bad Request yang menunjukkan formula wajib untuk metode kalkulasi formula |

### Scenario 8: Ekspor Company Component ke PDF

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company-component:read` | Autentikasi berhasil |
| 2 | Kirim `GET /api/v1/company-components/export?export_type=pdf` | Request diproses |
| 3 | Sistem menghasilkan file PDF dengan data company component | 200 OK dengan unduhan file PDF |
| 4 | Verifikasi file berisi kolom company code, component code, calculation method, tax type | Semua kolom yang diharapkan ada |
