import requests

API = "https://api.shop.example/v1"

def list_orders(page):
    return requests.get(f"{API}/orders", params={"page": page, "per_page": 50}).json()

def buy(sku):
    body = {"sku": sku, "qty": 1, "coupon": "WELCOME", "shipping_speed": "overnight"}
    return requests.post(API + "/orders", json=body).json()

def refund(order_id):
    return requests.post(f"{API}/orders/{order_id}/refund")
