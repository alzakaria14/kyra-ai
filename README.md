# Kyra

Kyra adalah agentic AI untuk customer service, marketing, dan sales WhatsApp. Aplikasi ini berjalan sebagai CLI dan webhook Go, memakai MySQL untuk riwayat/memori, Ollama lokal (`qwen3:4b-instruct-2507-q8_0`) untuk percakapan, RajaOngkir untuk ongkir, serta Xendit Invoice untuk pembayaran.

## Prasyarat

- Go 1.26+ dan MySQL 8+.
- Ollama aktif dan model tersedia: `ollama pull qwen3:4b-instruct-2507-q8_0`.
- Gateway WhatsApp di `C:\xampp\htdocs\whatsapp` telah berjalan pada port 5001.
- Kunci RajaOngkir dan Xendit (untuk pembuatan invoice nyata).

## Instalasi

1. Salin `.env.example` menjadi `.env`, lalu isi seluruh nilai MySQL dan API key.
2. Buat database MySQL, misalnya `CREATE DATABASE kyra CHARACTER SET utf8mb4;`.
3. Ambil dependensi dan kompilasi:

   ```powershell
   go mod download
   go build -o kyra.exe .
   ```

4. Buat tabel:

   ```powershell
   .\kyra.exe migrate
   ```

5. Jalankan server webhook:

   ```powershell
   .\kyra.exe serve
   ```

6. Pada `.env` gateway WhatsApp, set `WEBHOOK_BASE_URL=http://localhost:8080/webhook`, kemudian restart gateway.

## Hubungkan WhatsApp (QR)

Saat gateway dan Kyra sudah berjalan, jalankan:

```powershell
.\kyra.exe scan
```

Browser akan membuka QR pada `http://localhost:5001/session/start?session=kyra` (nama sesi mengikuti `WHATSAPP_SESSION`). Pindai dari WhatsApp perangkat utama. Jika browser tidak otomatis terbuka, salin URL yang dicetak CLI.

## Manajemen produk via CLI

Gunakan SKU unik tanpa spasi. Nama/deskripsi yang mengandung spasi perlu diberi tanda kutip.

```powershell
# Tambah produk: SKU, nama, harga rupiah, berat gram, deskripsi
.\kyra.exe product add KYRA-TSHIRT "Kaos Kyra" 125000 250 "Kaos katun premium"

# Daftar produk aktif
.\kyra.exe product list

# Ubah produk (seluruh detail diberikan kembali)
.\kyra.exe product edit KYRA-TSHIRT "Kaos Kyra Oversize" 135000 280 "Katun premium oversize"

# Hapus aman: produk dinonaktifkan, riwayat pesanan tidak hilang
.\kyra.exe product delete KYRA-TSHIRT
```

## Master kota pengiriman

Pelanggan cukup menyebutkan nama kota (dan provinsi bila perlu); Kyra tidak akan meminta maupun menyebut ID teknis. Sebelum menerima pesanan dari kota tersebut, masukkan mapping ID pengiriman sekali melalui CLI:

```powershell
# ID pengiriman, nama kota, provinsi
.\kyra.exe city add 33 "Banjarbaru" "Kalimantan Selatan"

# Cari atau lihat semua kota yang sudah tersedia
.\kyra.exe city list Banjarbaru
.\kyra.exe city list

# Ubah atau hapus mapping kota
.\kyra.exe city edit 33 "Banjarbaru" "Kalimantan Selatan"
.\kyra.exe city delete 33
```

Data ini disimpan dalam tabel `shipping_cities`. Saat invoice dibuat, Kyra mencari ID pengiriman berdasarkan kota/provinsi pelanggan dan menyimpan ID tersebut pada pesanan.

## Perilaku agent dan pesanan

Setiap pesan masuk disimpan di `conversations`. Data form pelanggan (`nama`, `alamat`, `kota`, dan `provinsi`) juga disimpan langsung di tabel `customers`, bukan hanya di ringkasan model. Pelanggan dapat menyalin lalu mengisi form dari Kyra; nilai yang sudah terisi tidak akan ditanyakan ulang. Riwayat percakapan terbaru, ringkasan, dan katalog dikirim ke Ollama pada setiap balasan agar pilihan produk tetap terbawa.

Agent menggunakan gaya bahasa Indonesia yang santai dan semi formal. Setelah data produk/kuantitas, nama penerima, alamat, dan kota tujuan lengkap, Kyra terlebih dahulu mengirim ringkasan pesanan berisi produk, ongkir, total pembayaran, dan alamat pengiriman. Invoice Xendit baru dibuat setelah pelanggan membalas `konfirmasi pesanan`. Kyra tidak menanyakan ekspedisi kepada pelanggan; sistem mencocokkan kota dengan master kota, menghitung total berat, lalu memilih tarif pengiriman paling murah secara otomatis.

Kyra hanya melayani percakapan terkait produk toko, pesanan, pengiriman, dan pembayaran. Pertanyaan di luar ruang lingkup—misalnya pembuatan program, politik, atau pengetahuan umum—ditolak secara sopan oleh program sebelum diteruskan ke Ollama, sehingga tidak memakai kapasitas GPU.

## Antrean Ollama

Pesan WhatsApp diproses secara FIFO oleh satu worker. Artinya, Kyra menunggu respons Ollama dan pengiriman balasan untuk satu pesan selesai sebelum memproses pesan berikutnya. Ini mencegah beberapa request Ollama berjalan bersamaan pada GPU. Kapasitas antrean dapat diatur melalui `MESSAGE_QUEUE_SIZE` (bawaan `100`); ketika penuh, webhook mengembalikan HTTP `503` agar gateway dapat mencoba mengirim ulang pesan tersebut.

`STORE_ORIGIN_CITY_ID` adalah ID asal pengiriman internal. Untuk keamanan, webhook Xendit memeriksa header `x-callback-token` terhadap `XENDIT_CALLBACK_TOKEN`; status `PAID`, `EXPIRED`, dan `FAILED` disimpan pada pesanan. Saat status berubah, Kyra mengirim notifikasi WhatsApp satu kali kepada customer: pembayaran diterima, pembayaran kedaluwarsa, atau pembayaran gagal.

## Uji daya Kyra dan Ollama

`power_benchmark.py` mengirim pesan `Halo` langsung ke endpoint lokal Kyra, kemudian Kyra memanggil Ollama. Jalur ini tidak melewati WhatsApp gateway, tidak mengirim pesan WhatsApp, dan tidak menulis riwayat/customer ke database. Skrip mengambil sensor dari LibreHardwareMonitor Web Server (`http://127.0.0.1:8085/data.json`) setiap 0,5 detik dan membuat `summary.json`, `telemetry.csv`, serta `failures.json`.

Build dan jalankan ulang server terlebih dahulu agar endpoint benchmark tersedia:

```powershell
go build -o kyra.exe .
.\kyra.exe serve
```

Di terminal lain jalankan (Python 3.9+; tidak perlu memasang package):

```powershell
python .\power_benchmark.py --count 1000 --message Halo
```

Jumlah pesan, interval sampling, dan baseline dapat diatur, misalnya `--count 100 --interval 0.25 --baseline-seconds 15`. Hasil menampilkan daya rata-rata CPU/GPU ketika idle dan saat beban, selisih dari baseline, daya puncak, energi total (Wh), energi per pesan sukses (Wh), suhu rata-rata/puncak, utilisasi, clock, putaran kipas GPU, throughput, dan latensi. Secara fisik energi per pesan adalah metrik yang tepat; watt adalah laju konsumsi daya sehingga dilaporkan sebagai rata-rata selama beban dan kenaikannya dari baseline.

Untuk benchmark remembering, gunakan skrip berikut. Request pemanasan membuat customer dan pesanan sintetis khusus benchmark di MySQL dengan status `paid`. Setiap request beban kemudian membaca remembering, riwayat, serta status pesanan tersebut dari MySQL dan mengirim konteksnya ke Ollama, tetap tanpa mengirim WhatsApp atau menulis percakapan.

```powershell
python .\power_benchmark_remembering.py --message "Bagaimana status pesanan saya?" --count 100
```

Hasilnya disimpan terpisah di `benchmark_results_remembering`. Rebuild dan restart server Kyra setelah memperbarui kode agar endpoint `/benchmark/remembering` tersedia.

## Callback Xendit PHP

Gunakan [callback/xendit.php](callback/xendit.php) sebagai URL callback publik Xendit, misalnya `https://domain-anda/kyra/callback/xendit.php`. File PHP ini berdiri sendiri dari server Go: ia memvalidasi `x-callback-token` terhadap `.env`, kemudian meneruskan body callback ke `http://127.0.0.1:PORT/webhook/xendit`. Pastikan Apache/PHP dan `kyra serve` berjalan pada mesin yang sama.

## Operasional

```powershell
# Lihat riwayat nomor tertentu
.\kyra.exe history 628123456789

# Lihat rincian pesanan
.\kyra.exe order 1
```

Untuk deployment, `PUBLIC_BASE_URL` harus dapat dijangkau gateway dan Xendit. Jangan commit `.env`; file tersebut sudah diabaikan Git.
