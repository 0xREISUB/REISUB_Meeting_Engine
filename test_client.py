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

# 3. Çıkış yap
logout()