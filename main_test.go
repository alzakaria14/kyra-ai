package main

import (
	"strings"
	"testing"
)

func TestParseCustomerForm(t *testing.T) {
	got := parseCustomerForm("Boleh isi data berikut ya:\nNama: Al Zakaria\nAlamat lengkap: Jl. Sungai Bakung\nKota: Banjarmasin\nProvinsi: Kalimantan Selatan")
	if got.Name != "Al Zakaria" || got.Address != "Jl. Sungai Bakung" || got.City != "Banjarmasin" || got.Province != "Kalimantan Selatan" {
		t.Fatalf("form tidak terbaca dengan benar: %#v", got)
	}
}

func TestMergeCustomerDataKeepsSavedForm(t *testing.T) {
	customer := Customer{Name: "Al Zakaria", ShippingAddress: "Jl. Sungai Bakung", DestinationCity: "Banjarmasin", Province: "Kalimantan Selatan"}
	ai := AIReply{RecipientName: "Nama yang keliru"}
	mergeCustomerData(&ai, customer)
	if ai.RecipientName != customer.Name || ai.ShippingAddress != customer.ShippingAddress || ai.DestinationCity != customer.DestinationCity || ai.DestinationProvince != customer.Province {
		t.Fatalf("data pelanggan tidak dipertahankan: %#v", ai)
	}
}

func TestOrderConfirmationMessage(t *testing.T) {
	message := orderConfirmationMessage(OrderSummary{
		RecipientName: "Al Zakaria", ShippingAddress: "Jl. Sungai Bakung", City: "Banjarmasin",
		ShippingCost: 12000, Total: 137000, Items: []OrderSummaryItem{{Name: "Kaos Dayak", Quantity: 1}},
	})
	for _, text := range []string{"Kaos Dayak x1", "Ongkir: Rp12000", "Total pembayaran: Rp137000", "Nama penerima: Al Zakaria", "Alamat: Jl. Sungai Bakung", "Kota: Banjarmasin", "konfirmasi pesanan"} {
		if !strings.Contains(message, text) {
			t.Fatalf("ringkasan tidak memuat %q: %s", text, message)
		}
	}
}

func TestExpiredPaymentMessageIsFriendly(t *testing.T) {
	message := paymentStatusMessage("expired")
	for _, text := range []string{"Maaf", "jangan sungkan", "menghubungi saya kembali"} {
		if !strings.Contains(message, text) {
			t.Fatalf("pesan expired belum ramah atau lengkap: %s", message)
		}
	}
}
