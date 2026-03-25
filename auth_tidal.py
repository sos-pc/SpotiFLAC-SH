#!/usr/bin/env python3
import argparse
import json
import sys
import urllib.request
import webbrowser


def main():
    parser = argparse.ArgumentParser(
        description="Authenticate SpotiFLAC with Tidal using PKCE Flow."
    )
    parser.add_argument(
        "--host",
        default="http://localhost:6890",
        help="The URL of your SpotiFLAC instance (e.g. http://192.168.1.10:6890)",
    )
    parser.add_argument(
        "--password",
        default=None,
        help="Your SpotiFLAC API password (if authentication is enabled)",
    )
    args = parser.parse_args()

    host = args.host.rstrip("/")

    print("==========================================")
    print("  SpotiFLAC - Tidal PKCE Authentication   ")
    print("==========================================")
    print(f"Connecting to SpotiFLAC at: {host}")

    headers = {}
    if args.password:
        headers["Authorization"] = f"Bearer {args.password}"

    # 1. Ask SpotiFLAC to generate the secure login URL
    print("\n[1/3] Requesting secure authentication URL from SpotiFLAC...")
    req = urllib.request.Request(f"{host}/api/auth/tidal/url", headers=headers)
    try:
        with urllib.request.urlopen(req) as response:
            data = json.loads(response.read().decode())
            auth_url = data.get("url")
    except urllib.error.HTTPError as e:
        print(f"❌ Error: SpotiFLAC returned HTTP {e.code}.")
        if e.code in [401, 403]:
            print(
                "Hint: You might need to provide the SpotiFLAC password using --password=YOUR_PASSWORD"
            )
        return
    except Exception as e:
        print(
            f"❌ Error: Could not connect to SpotiFLAC. Is the server running at {host}?"
        )
        print(f"Details: {e}")
        return

    if not auth_url:
        print("❌ Error: Invalid response from server (no URL provided).")
        return

    print("\n------------------------------------------------------------------")
    print("🚨 ACTION REQUIRED 🚨")
    print(
        "1. Log in with your Tidal Premium account in the browser window that just opened."
    )
    print("   (If it didn't open, manually go to the link below)")
    print("2. You will be redirected to a blank page or the web player.")
    print("3. Copy the ENTIRE URL from your browser's address bar.")
    print("   (It should look like: https://listen.tidal.com/login/auth?code=...)")
    print("------------------------------------------------------------------\n")
    print(f"Link: {auth_url}\n")

    # Try to automatically open the browser
    try:
        webbrowser.open(auth_url)
    except:
        pass

    redirect_url = input("Paste the redirected URL here: ").strip()

    if not redirect_url:
        print("❌ Error: URL cannot be empty.")
        return

    # 2. Send the code back to SpotiFLAC
    print("\n[2/3] Sending authorization code back to SpotiFLAC...")

    payload = json.dumps({"url": redirect_url}).encode("utf-8")
    headers["Content-Type"] = "application/json"

    req = urllib.request.Request(
        f"{host}/api/auth/tidal/callback", data=payload, headers=headers, method="POST"
    )
    try:
        with urllib.request.urlopen(req) as response:
            data = json.loads(response.read().decode())
            if data.get("success"):
                print("[3/3] ✅ SUCCESS! Tidal token has been securely saved.")
                print(
                    "SpotiFLAC can now download Lossless FLACs directly from the official Tidal API."
                )
                print("You can close this script.")
            else:
                print(f"❌ ERROR: Failed to authenticate. Server responded: {data}")
    except urllib.error.HTTPError as e:
        error_msg = e.read().decode()
        print(f"❌ ERROR: Failed to authenticate (HTTP {e.code}).")
        print(f"Server responded: {error_msg}")
        print(
            "Hint: Make sure you copied the ENTIRE url containing the 'code=' parameter."
        )
    except Exception as e:
        print(f"❌ ERROR: Failed to send code to SpotiFLAC.")
        print(f"Details: {e}")


if __name__ == "__main__":
    main()
