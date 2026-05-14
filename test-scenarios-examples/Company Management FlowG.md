# Test Scenarios: Company Management

## Preconditions

- User yang terautentikasi memiliki JWT token yang valid
- User yang terautentikasi memiliki permission yang sesuai (ReadCompany, SyncCompany sesuai kebutuhan)
- Minimal satu record company ada di database utama (sebelumnya disinkronkan dari PI-Smart)
- Database PI-Smart dapat diakses untuk skenario terkait sinkronisasi
- Company uji coba sudah dikonfigurasi dengan benar

## Scenarios

### Scenario 1: Daftar Company dengan Pagination

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company:read` dan dibatasi pada company tertentu | Autentikasi berhasil |
| 2 | Kirim `GET /api/v1/companies?page=1&limit=10` | Request diproses |
| 3 | Sistem memfilter company berdasarkan cakupan company user | Hanya company yang dapat diakses yang disertakan |
| 4 | Sistem mengembalikan daftar company dengan pagination | 200 OK dengan daftar company dan metadata pagination (total, page, limit) |

### Scenario 2: Mendapatkan Detail Company

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company:read` | Autentikasi berhasil |
| 2 | Kirim `GET /api/v1/companies/:id` dengan ID company yang valid | Request diproses |
| 3 | Sistem mencari company berdasarkan ID | Company ditemukan |
| 4 | Sistem mengembalikan detail lengkap company | 200 OK dengan semua field: name, code, address, thumbnail_url, logo_url, business_sector, short_name |

### Scenario 3: Mendapatkan Detail Company dengan ID yang Tidak Ada

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company:read` | Autentikasi berhasil |
| 2 | Kirim `GET /api/v1/companies/:id` dengan UUID yang tidak ada | Request diproses |
| 3 | Sistem mencari company berdasarkan ID | Company tidak ditemukan |
| 4 | Sistem mengembalikan response error | 404 Not Found error |

### Scenario 4: Ekspor Company ke PDF

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company:read` | Autentikasi berhasil |
| 2 | Pastikan jumlah record company dalam cakupan kurang dari 1000 | Prasyarat terpenuhi |
| 3 | Kirim `GET /api/v1/companies/export?export_type=pdf` | Request diproses |
| 4 | Sistem menghasilkan file PDF yang berisi data company | 200 OK dengan unduhan file PDF |

### Scenario 5: Ekspor Company ke XLSX

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company:read` | Autentikasi berhasil |
| 2 | Kirim `GET /api/v1/companies/export?export_type=xlsx` | Request diproses |
| 3 | Sistem menghasilkan file XLSX yang berisi data company | 200 OK dengan unduhan file XLSX |

### Scenario 6: Ekspor Company dengan Tipe Tidak Valid

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company:read` | Autentikasi berhasil |
| 2 | Kirim `GET /api/v1/companies/export?export_type=csv` | Request diproses |
| 3 | Sistem memvalidasi parameter export_type | Tipe tidak valid terdeteksi |
| 4 | Sistem mengembalikan response error | 400 Bad Request error yang menunjukkan tipe yang valid adalah `pdf` dan `xlsx` |

### Scenario 7: Memicu Sinkronisasi Company dari PI-Smart

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang memiliki permission `company:sync` | Autentikasi berhasil |
| 2 | Kirim `GET /api/v1/companies/sync` | Request diproses |
| 3 | Sistem mendelegasikan tugas sinkronisasi ke background job queue melalui `singleton.Delegate()` | Job berhasil di-queue |
| 4 | Sistem mengembalikan konfirmasi langsung | 200 OK dengan pesan yang menunjukkan job sinkronisasi telah di-queue |
| 5 | Background job terhubung ke PI-Smart DB, mengambil company, dan melakukan upsert ke DB utama | Record company dibuat atau diperbarui berdasarkan `code` yang unik |

### Scenario 8: Mengakses Endpoint Company Tanpa Permission

| Step | Action | Expected Result |
| ---- | ------ | --------------- |
| 1 | Autentikasi sebagai user yang TIDAK memiliki permission `company:read` | Autentikasi berhasil |
| 2 | Kirim `GET /api/v1/companies` | Request diproses |
| 3 | Sistem memeriksa permissions | Permission `company:read` tidak ada |
| 4 | Sistem mengembalikan response error | 403 Forbidden error |
