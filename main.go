package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type Config struct{ Port, DSN, WAURL, WASession, OllamaURL, OllamaModel, RajaKey, RajaURL, OriginCity, OriginAddress, XenditKey, XenditToken, SuccessURL, FailureURL string }
type App struct {
	db     *sql.DB
	cfg    Config
	client *http.Client
}
type Product struct {
	ID                     int64
	SKU, Name, Description string
	Price                  int64
	Weight                 int
}
type Customer struct {
	ID                  int64
	Phone, Name, Memory string
}
type LineItem struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}
type AIReply struct {
	Reply             string     `json:"reply"`
	Memory            string     `json:"memory"`
	CreateInvoice     bool       `json:"create_invoice"`
	Items             []LineItem `json:"items"`
	RecipientName     string     `json:"recipient_name"`
	ShippingAddress   string     `json:"shipping_address"`
	DestinationCityID string     `json:"destination_city_id"`
	Courier           string     `json:"courier"`
	ShippingService   string     `json:"shipping_service"`
}
type inboundMessage struct {
	Session string `json:"session"`
	From    string `json:"from"`
	Message string `json:"message"`
}
type xenditEvent struct {
	ID         string `json:"id"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
	InvoiceURL string `json:"invoice_url"`
}

func main() {
	loadDotEnv(".env")
	cfg := config()
	if len(os.Args) < 2 {
		usage()
		return
	}
	if os.Args[1] == "scan" {
		scanQR(cfg)
		return
	}
	if cfg.DSN == "" {
		log.Fatal("MYSQL_DSN wajib diisi di .env")
	}
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err = db.Ping(); err != nil {
		log.Fatalf("gagal terhubung MySQL: %v", err)
	}
	a := &App{db: db, cfg: cfg, client: &http.Client{Timeout: 30 * time.Second}}
	switch os.Args[1] {
	case "migrate":
		must(a.migrate())
	case "serve":
		must(a.serve())
	case "product":
		must(a.productCmd(os.Args[2:]))
	case "history":
		must(a.historyCmd(os.Args[2:]))
	case "order":
		must(a.orderCmd(os.Args[2:]))
	default:
		usage()
	}
}

func config() Config {
	return Config{Port: env("PORT", "8080"), DSN: os.Getenv("MYSQL_DSN"), WAURL: strings.TrimRight(env("WHATSAPP_BASE_URL", "http://localhost:5001"), "/"), WASession: env("WHATSAPP_SESSION", "kyra"), OllamaURL: strings.TrimRight(env("OLLAMA_BASE_URL", "http://localhost:11434"), "/"), OllamaModel: env("OLLAMA_MODEL", "qwen3:4b-instruct-2507-q8_0"), RajaKey: os.Getenv("RAJAONGKIR_API_KEY"), RajaURL: strings.TrimRight(env("RAJAONGKIR_BASE_URL", "https://api.rajaongkir.com/starter"), "/"), OriginCity: os.Getenv("STORE_ORIGIN_CITY_ID"), OriginAddress: os.Getenv("STORE_ORIGIN_ADDRESS"), XenditKey: os.Getenv("XENDIT_SECRET_KEY"), XenditToken: os.Getenv("XENDIT_CALLBACK_TOKEN"), SuccessURL: os.Getenv("XENDIT_SUCCESS_REDIRECT_URL"), FailureURL: os.Getenv("XENDIT_FAILURE_REDIRECT_URL")}
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Print(`Kyra — AI customer service, marketing & sales

Perintah:
  kyra migrate                         membuat/memperbarui tabel MySQL
  kyra serve                           menjalankan webhook di PORT
  kyra scan [nama-sesi]                membuka halaman QR WhatsApp
  kyra product list
  kyra product add <sku> <nama> <harga> <berat_gram> <deskripsi>
  kyra product edit <sku> <nama> <harga> <berat_gram> <deskripsi>
  kyra product delete <sku>
  kyra history <nomor> [limit]
  kyra order <id>                      rincian pesanan
`)
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) == 2 {
			os.Setenv(strings.TrimSpace(kv[0]), strings.Trim(strings.TrimSpace(kv[1]), "\"'"))
		}
	}
}

func (a *App) migrate() error {
	b, err := os.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	for _, stmt := range strings.Split(string(b), ";") {
		if strings.TrimSpace(stmt) != "" {
			if _, err = a.db.Exec(stmt); err != nil {
				return err
			}
		}
	}
	fmt.Println("Migrasi selesai.")
	return nil
}

func scanQR(cfg Config) {
	session := cfg.WASession
	if len(os.Args) > 2 && strings.TrimSpace(os.Args[2]) != "" {
		session = os.Args[2]
	}
	target := cfg.WAURL + "/session/start?session=" + url.QueryEscape(session)
	fmt.Println("Pindai QR WhatsApp di:", target)
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start(); err != nil {
		fmt.Println("Browser tidak dibuka otomatis; buka URL di atas secara manual.")
	}
}

func (a *App) productCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("gunakan: product list|add|edit|delete")
	}
	switch args[0] {
	case "list":
		rows, err := a.db.Query("SELECT id,sku,name,description,price,weight_gram FROM products WHERE active=1 ORDER BY name")
		if err != nil {
			return err
		}
		defer rows.Close()
		fmt.Printf("%-5s %-14s %-28s %-14s %s\n", "ID", "SKU", "NAMA", "HARGA", "BERAT(g)")
		for rows.Next() {
			var p Product
			if err := rows.Scan(&p.ID, &p.SKU, &p.Name, &p.Description, &p.Price, &p.Weight); err != nil {
				return err
			}
			fmt.Printf("%-5d %-14s %-28s Rp%-12d %d\n", p.ID, p.SKU, p.Name, p.Price, p.Weight)
		}
		return rows.Err()
	case "add", "edit":
		if len(args) != 6 {
			return errors.New("format: product " + args[0] + " <sku> <nama> <harga> <berat_gram> <deskripsi>")
		}
		price, e1 := strconv.ParseInt(args[3], 10, 64)
		weight, e2 := strconv.Atoi(args[4])
		if e1 != nil || e2 != nil || price < 0 || weight < 0 {
			return errors.New("harga dan berat harus angka positif")
		}
		if args[0] == "add" {
			_, err := a.db.Exec("INSERT INTO products (sku,name,price,weight_gram,description) VALUES (?,?,?,?,?)", args[1], args[2], price, weight, args[5])
			if err == nil {
				fmt.Println("Produk ditambahkan.")
			}
			return err
		}
		r, err := a.db.Exec("UPDATE products SET name=?,price=?,weight_gram=?,description=?,active=1 WHERE sku=?", args[2], price, weight, args[5], args[1])
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			return errors.New("SKU tidak ditemukan")
		}
		fmt.Println("Produk diperbarui.")
		return nil
	case "delete":
		if len(args) != 2 {
			return errors.New("format: product delete <sku>")
		}
		r, err := a.db.Exec("UPDATE products SET active=0 WHERE sku=?", args[1])
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			return errors.New("SKU tidak ditemukan")
		}
		fmt.Println("Produk dinonaktifkan.")
		return nil
	}
	return errors.New("sub-perintah produk tidak dikenal")
}

func (a *App) historyCmd(args []string) error {
	if len(args) < 1 {
		return errors.New("format: history <nomor> [limit]")
	}
	limit := 20
	if len(args) > 1 {
		var e error
		limit, e = strconv.Atoi(args[1])
		if e != nil || limit < 1 || limit > 200 {
			return errors.New("limit 1-200")
		}
	}
	rows, err := a.db.Query("SELECT c.direction,c.body,c.created_at FROM conversations c JOIN customers u ON u.id=c.customer_id WHERE u.phone=? ORDER BY c.created_at DESC LIMIT ?", normalPhone(args[0]), limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var d, b string
		var t time.Time
		if err := rows.Scan(&d, &b, &t); err != nil {
			return err
		}
		fmt.Printf("[%s] %s: %s\n", t.Format("2006-01-02 15:04"), d, b)
	}
	return rows.Err()
}
func (a *App) orderCmd(args []string) error {
	if len(args) != 1 {
		return errors.New("format: order <id>")
	}
	id, e := strconv.ParseInt(args[0], 10, 64)
	if e != nil {
		return e
	}
	var status string
	var total int64
	var link sql.NullString
	err := a.db.QueryRow("SELECT status,total,xendit_invoice_url FROM orders WHERE id=?", id).Scan(&status, &total, &link)
	if err != nil {
		return err
	}
	fmt.Printf("Pesanan #%d | %s | Total Rp%d\n%s\n", id, status, total, link.String)
	rows, err := a.db.Query("SELECT product_name,quantity,unit_price FROM order_items WHERE order_id=?", id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		var q int
		var p int64
		rows.Scan(&n, &q, &p)
		fmt.Printf("- %dx %s @ Rp%d\n", q, n, p)
	}
	return rows.Err()
}

func (a *App) serve() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/webhook/message", a.messageWebhook)
	mux.HandleFunc("/webhook/session", a.sessionWebhook)
	mux.HandleFunc("/webhook/xendit", a.xenditWebhook)
	s := &http.Server{Addr: ":" + a.cfg.Port, Handler: logging(mux), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("Kyra berjalan di http://localhost:%s", a.cfg.Port)
	return s.ListenAndServe()
}
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, r) })
}

func (a *App) messageWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in inboundMessage
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	// Gateway juga mengirim event media tanpa caption atau event WhatsApp lain
	// yang tidak memiliki teks. Event ini bukan pesan yang dapat dijawab agent.
	if strings.TrimSpace(in.Message) == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	phone := normalPhone(in.From)
	if phone == "" {
		http.Error(w, "invalid sender", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	if err := a.respond(ctx, phone, in.Message); err != nil {
		log.Printf("pesan dari %s gagal: %v", phone, err)
		http.Error(w, "processing failed", 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) sessionWebhook(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) xenditWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if a.cfg.XenditToken != "" && r.Header.Get("x-callback-token") != a.cfg.XenditToken {
		http.Error(w, "unauthorized", 401)
		return
	}
	var e xenditEvent
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&e); err != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	status := map[string]string{"PAID": "paid", "EXPIRED": "expired", "FAILED": "failed"}[strings.ToUpper(e.Status)]
	if status != "" {
		_, err := a.db.Exec("UPDATE orders SET status=?, xendit_invoice_url=COALESCE(NULLIF(?,''),xendit_invoice_url) WHERE xendit_invoice_id=? OR xendit_external_id=?", status, e.InvoiceURL, e.ID, e.ExternalID)
		if err != nil {
			http.Error(w, "db error", 500)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) respond(ctx context.Context, phone, message string) error {
	cust, err := a.customer(phone)
	if err != nil {
		return err
	}
	if err = a.saveMessage(cust.ID, "inbound", message); err != nil {
		return err
	}
	products, err := a.products()
	if err != nil {
		return err
	}
	ai, err := a.askLLM(ctx, cust, products, message)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ai.Memory) != "" {
		if _, err = a.db.Exec("UPDATE customers SET memory=? WHERE id=?", ai.Memory, cust.ID); err != nil {
			return err
		}
	}
	reply := strings.TrimSpace(ai.Reply)
	if ai.CreateInvoice {
		link, err := a.createInvoice(ctx, cust, ai, products)
		if err != nil {
			log.Printf("invoice ditolak: %v", err)
			if reply == "" {
				reply = "Data pesanan belum lengkap. Mohon kirim nama penerima, alamat, kota tujuan, serta pilihan kurir."
			}
		} else {
			reply = strings.TrimSpace(reply + "\n\nSilakan selesaikan pembayaran melalui: " + link)
		}
	}
	if reply == "" {
		reply = "Halo, ada yang bisa Kyra bantu terkait produk atau pesanan Anda?"
	}
	if err = a.sendWA(ctx, phone, reply); err != nil {
		return err
	}
	return a.saveMessage(cust.ID, "outbound", reply)
}
func (a *App) customer(phone string) (Customer, error) {
	var c Customer
	err := a.db.QueryRow("SELECT id,phone,COALESCE(name,''),memory FROM customers WHERE phone=?", phone).Scan(&c.ID, &c.Phone, &c.Name, &c.Memory)
	if err == sql.ErrNoRows {
		r, e := a.db.Exec("INSERT INTO customers (phone,memory) VALUES (?,?)", phone, "Pelanggan baru; belum ada pesanan.")
		if e != nil {
			return c, e
		}
		id, _ := r.LastInsertId()
		return Customer{ID: id, Phone: phone, Memory: "Pelanggan baru; belum ada pesanan."}, nil
	}
	return c, err
}
func (a *App) saveMessage(customerID int64, direction, body string) error {
	_, err := a.db.Exec("INSERT INTO conversations (customer_id,direction,body) VALUES (?,?,?)", customerID, direction, body)
	return err
}
func (a *App) products() ([]Product, error) {
	rows, err := a.db.Query("SELECT id,sku,name,description,price,weight_gram FROM products WHERE active=1 ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var p []Product
	for rows.Next() {
		var x Product
		if err = rows.Scan(&x.ID, &x.SKU, &x.Name, &x.Description, &x.Price, &x.Weight); err != nil {
			return nil, err
		}
		p = append(p, x)
	}
	return p, rows.Err()
}

func (a *App) askLLM(ctx context.Context, c Customer, products []Product, message string) (AIReply, error) {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type req struct {
		Model    string `json:"model"`
		Messages []msg  `json:"messages"`
		Format   string `json:"format"`
		Stream   bool   `json:"stream"`
	}
	var catalog strings.Builder
	for _, p := range products {
		fmt.Fprintf(&catalog, "SKU=%s | %s | Rp%d | %dg | %s\n", p.SKU, p.Name, p.Price, p.Weight, p.Description)
	}
	system := `Anda adalah Kyra, customer service, marketing, dan sales berbahasa Indonesia. Jawab hangat, singkat, dan akurat HANYA dari katalog. Jangan membuat harga, stok, diskon atau status pembayaran. Kumpulkan data pesanan: SKU serta kuantitas, nama penerima, alamat lengkap, ID kota tujuan RajaOngkir, kurir, layanan kurir. Buat invoice hanya setelah semua data lengkap dan pelanggan telah mengonfirmasi pesanan. Jika belum lengkap, create_invoice=false dan jelaskan yang masih dibutuhkan. memory adalah ringkasan faktual singkat pelanggan/pesanan yang diperbarui. Balas HANYA JSON valid dengan bentuk: {"reply":"...","memory":"...","create_invoice":false,"items":[{"sku":"","quantity":1}],"recipient_name":"","shipping_address":"","destination_city_id":"","courier":"jne","shipping_service":"REG"}.` + "\n\nKATALOG:\n" + catalog.String() + "\nMEMORI PELANGGAN:\n" + c.Memory
	payload := req{Model: a.cfg.OllamaModel, Messages: []msg{{Role: "system", Content: system}, {Role: "user", Content: message}}, Format: "json", Stream: false}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Error string `json:"error"`
	}
	if err := a.postJSON(ctx, a.cfg.OllamaURL+"/api/chat", payload, &out, nil); err != nil {
		return AIReply{}, err
	}
	if out.Error != "" {
		return AIReply{}, errors.New(out.Error)
	}
	var ai AIReply
	if err := json.Unmarshal([]byte(stripFence(out.Message.Content)), &ai); err != nil {
		return ai, fmt.Errorf("jawaban Ollama bukan JSON valid: %w", err)
	}
	return ai, nil
}
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func (a *App) createInvoice(ctx context.Context, c Customer, ai AIReply, products []Product) (string, error) {
	if len(ai.Items) == 0 || ai.RecipientName == "" || ai.ShippingAddress == "" || ai.DestinationCityID == "" || ai.Courier == "" || ai.ShippingService == "" {
		return "", errors.New("data invoice tidak lengkap")
	}
	bySKU := map[string]Product{}
	for _, p := range products {
		bySKU[strings.ToUpper(p.SKU)] = p
	}
	var subtotal int64
	weight := 0
	type resolved struct {
		P Product
		Q int
	}
	rs := []resolved{}
	for _, it := range ai.Items {
		p, ok := bySKU[strings.ToUpper(it.SKU)]
		if !ok || it.Quantity < 1 || it.Quantity > 100 {
			return "", fmt.Errorf("item tidak valid: %s", it.SKU)
		}
		rs = append(rs, resolved{p, it.Quantity})
		subtotal += p.Price * int64(it.Quantity)
		weight += p.Weight * it.Quantity
	}
	shipping, err := a.shippingCost(ctx, ai.DestinationCityID, weight, ai.Courier, ai.ShippingService)
	if err != nil {
		return "", err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, "INSERT INTO orders (customer_id,status,recipient_name,recipient_phone,shipping_address,destination_city_id,courier,shipping_service,shipping_cost,subtotal,total) VALUES (?,'draft',?,?,?,?,?,?,?,?,?)", c.ID, ai.RecipientName, c.Phone, ai.ShippingAddress, ai.DestinationCityID, strings.ToLower(ai.Courier), ai.ShippingService, shipping, subtotal, subtotal+shipping)
	if err != nil {
		return "", err
	}
	oid, _ := r.LastInsertId()
	for _, x := range rs {
		if _, err = tx.ExecContext(ctx, "INSERT INTO order_items (order_id,product_id,product_name,quantity,unit_price,weight_gram) VALUES (?,?,?,?,?,?)", oid, x.P.ID, x.P.Name, x.Q, x.P.Price, x.P.Weight); err != nil {
			return "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	external := fmt.Sprintf("kyra-%d-%d", oid, time.Now().Unix())
	link, invoiceID, err := a.xenditInvoice(ctx, external, subtotal+shipping, c.Phone, ai.RecipientName)
	if err != nil {
		return "", err
	}
	_, err = a.db.ExecContext(ctx, "UPDATE orders SET status='awaiting_payment',xendit_external_id=?,xendit_invoice_id=?,xendit_invoice_url=? WHERE id=?", external, invoiceID, link, oid)
	return link, err
}

func (a *App) shippingCost(ctx context.Context, destination string, weight int, courier, service string) (int64, error) {
	if a.cfg.RajaKey == "" || a.cfg.OriginCity == "" {
		return 0, errors.New("RAJAONGKIR_API_KEY dan STORE_ORIGIN_CITY_ID wajib diisi")
	}
	if weight < 1 {
		weight = 1
	}
	form := url.Values{"origin": {a.cfg.OriginCity}, "destination": {destination}, "weight": {strconv.Itoa(weight)}, "courier": {strings.ToLower(courier)}}
	req, err := http.NewRequestWithContext(ctx, "POST", a.cfg.RajaURL+"/cost", strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("key", a.cfg.RajaKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	var out struct {
		Rajaongkir struct {
			Results []struct {
				Costs []struct {
					Service string `json:"service"`
					Cost    []struct {
						Value int64 `json:"value"`
					} `json:"cost"`
				} `json:"costs"`
			} `json:"results"`
			Status struct {
				Description string `json:"description"`
			} `json:"status"`
		} `json:"rajaongkir"`
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return 0, readAPIError(res)
	}
	if err = json.NewDecoder(res.Body).Decode(&out); err != nil {
		return 0, err
	}
	for _, r := range out.Rajaongkir.Results {
		for _, cost := range r.Costs {
			if strings.EqualFold(cost.Service, service) && len(cost.Cost) > 0 {
				return cost.Cost[0].Value, nil
			}
		}
	}
	return 0, fmt.Errorf("layanan %s tidak ditemukan pada kurir %s", service, courier)
}
func (a *App) xenditInvoice(ctx context.Context, external string, amount int64, phone, name string) (string, string, error) {
	if a.cfg.XenditKey == "" {
		return "", "", errors.New("XENDIT_SECRET_KEY wajib diisi")
	}
	body := map[string]any{"external_id": external, "amount": amount, "currency": "IDR", "description": "Pembayaran pesanan Kyra " + external, "customer": map[string]string{"given_names": name, "mobile_number": "+" + phone}}
	if a.cfg.SuccessURL != "" {
		body["success_redirect_url"] = a.cfg.SuccessURL
	}
	if a.cfg.FailureURL != "" {
		body["failure_redirect_url"] = a.cfg.FailureURL
	}
	var out struct {
		ID         string `json:"id"`
		InvoiceURL string `json:"invoice_url"`
	}
	headers := http.Header{}
	headers.Set("Authorization", "Basic "+basicAuth(a.cfg.XenditKey+":"))
	if err := a.postJSON(ctx, "https://api.xendit.co/v2/invoices", body, &out, headers); err != nil {
		return "", "", err
	}
	if out.ID == "" || out.InvoiceURL == "" {
		return "", "", errors.New("respons invoice Xendit tidak lengkap")
	}
	return out.InvoiceURL, out.ID, nil
}
func basicAuth(s string) string {
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var b strings.Builder
	for i := 0; i < len(s); i += 3 {
		n := int(s[i]) << 16
		if i+1 < len(s) {
			n |= int(s[i+1]) << 8
		}
		if i+2 < len(s) {
			n |= int(s[i+2])
		}
		b.WriteByte(table[(n>>18)&63])
		b.WriteByte(table[(n>>12)&63])
		if i+1 < len(s) {
			b.WriteByte(table[(n>>6)&63])
		} else {
			b.WriteByte('=')
		}
		if i+2 < len(s) {
			b.WriteByte(table[n&63])
		} else {
			b.WriteByte('=')
		}
	}
	return b.String()
}
func (a *App) sendWA(ctx context.Context, to, text string) error {
	var out any
	return a.postJSON(ctx, a.cfg.WAURL+"/message/send-text", map[string]any{"session": a.cfg.WASession, "to": to, "text": text}, &out, nil)
}
func (a *App) postJSON(ctx context.Context, target string, payload, out any, extra http.Header) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if extra != nil {
		for k, v := range extra {
			req.Header[k] = v
		}
	}
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return readAPIError(res)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}
func readAPIError(r *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
	return fmt.Errorf("API %s: %s", r.Status, strings.TrimSpace(string(b)))
}
func normalPhone(v string) string {
	v = strings.TrimSpace(strings.Split(v, "@")[0])
	v = strings.NewReplacer("+", "", "-", "", " ", "", "(", "", ")", "").Replace(v)
	if strings.HasPrefix(v, "0") {
		v = "62" + v[1:]
	}
	return v
}
