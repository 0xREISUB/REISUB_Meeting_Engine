import requests
import time

BASE_URL = "http://localhost:8080"
TOKEN = ""

def print_separator(title):
    print(f"\n{'-'*15} {title} {'-'*15}")

def register(nick, password):
    print_separator("KAYIT OLUYOR")
    payload = {"nick": nick, "password": password}
    response = requests.post(f"{BASE_URL}/register", json=payload)
    print("Durum:", response.status_code)
    print("Cevap:", response.json())

def login(nick, password):
    global TOKEN
    print_separator("GİRİŞ YAPILIYOR")
    payload = {"nick": nick, "password": password}
    response = requests.post(f"{BASE_URL}/login", json=payload)
    print("Durum:", response.status_code)
    data = response.json()
    print("Cevap:", data)
    
    if "token" in data:
        TOKEN = data["token"]
        print(f"--> Yeni Token Alındı: {TOKEN[:15]}...")

def get_users():
    print_separator("VERİTABANINDAKİ KULLANICILAR")
    response = requests.get(f"{BASE_URL}/users")
    users = response.json().get("users", [])
    
    for u in users:
        print(f"ID: {u['ID']} | Nick: {u['Nick']}")
        print(f"Token: {u['Token'] if u['Token'] else '[BOS - CIKIS YAPILMIS]'}")
        print(f"Tarih: {u['CreatedAt']}")
        print("-")

def logout():
    global TOKEN
    print_separator("ÇIKIŞ YAPILIYOR")
    headers = {"Authorization": TOKEN}
    response = requests.post(f"{BASE_URL}/logout", headers=headers)
    print("Durum:", response.status_code)
    print("Cevap:", response.json())
    TOKEN = ""

# --- TEST SENARYOSU ---
test_nick = "tester1"
test_pass = "gizlisifre123"

# 1. Kayıt ol (Aynı nick varsa hata verir, sorun değil devam eder)
register(test_nick, test_pass)
time.sleep(1)

# 2. Giriş yap ve token al
login(test_nick, test_pass)
time.sleep(1)

# 3. Veritabanını kontrol et (Token dolu olmalı)
get_users()
time.sleep(1)

# 4. Çıkış yap
logout()
time.sleep(1)

# 5. Veritabanını tekrar kontrol et (Token silinmiş olmalı)
get_users()