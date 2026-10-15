#!/usr/bin/env python3
"""traffic.py N - sends N invented lumbar spine MRI requests to the public demonstration, as its PAS server would.

Everything is invented: the patient is "Test Synthetic", born 1970-01-01, and each request answers the policy's questions
at random (a question left unanswered is evidence nobody addressed, as in a real request). Standard library only.
Reads the bearer token from /opt/vaglio/conf/token.
"""
import json
import random
import ssl
import sys
import time
import urllib.request
import os
import uuid

# The instance's own name and files; set apart only to try the script elsewhere.
def _url():
    try:
        return open("/opt/vaglio/conf/url").read().strip()
    except OSError:
        return "https://vaglio.perfuse.health:8443"


URL = os.environ.get("VAGLIO_URL") or _url()
CONF = os.environ.get("VAGLIO_CONF", "/opt/vaglio/conf")
CA = os.environ.get("VAGLIO_CA")  # a private authority to trust, for a local try
QUESTIONS = [
    ("red-flag", "Any red-flag finding (cauda equina, progressive deficit, infection, cancer, fracture)?", 0.15),
    ("conservative-therapy", "Six weeks of conservative therapy without improvement?", 0.6),
    ("surgical-planning", "MRI to plan a scheduled surgery or intervention?", 0.1),
    ("prior-imaging-6mo", "Lumbar MRI in the last six months?", 0.35),
    ("new-or-worsening", "Symptoms new or worse since the prior study?", 0.4),
]
STATES = ["CA", "TX", "NY", "FL", "WA", "IL", "AZ", "MD"]


def request():
    b = json.load(open(os.path.join(CONF, "template.json")))
    claim = b["entry"][0]["resource"]
    claim["identifier"] = [{"system": "http://provider.example/claims", "value": "DEMO-%06d" % random.randrange(10**6)}]
    claim["created"] = time.strftime("%Y-%m-%d", time.gmtime())
    if random.random() < 0.15:
        claim["priority"] = {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/processpriority", "code": "stat"}]}
    b["entry"][1]["resource"]["address"] = [{"use": "home", "state": random.choice(STATES)}]
    items = []
    for link, text, yes in QUESTIONS:
        if random.random() < 0.15:
            continue  # not addressed
        items.append({"linkId": link, "text": text, "answer": [{"valueBoolean": random.random() < yes}]})
    qr = b["entry"][4]["resource"]
    qr["item"] = items
    return b


def main():
    n = int(sys.argv[1]) if len(sys.argv) > 1 else 1
    token = open(os.path.join(CONF, "token")).read().strip()
    ctx = ssl.create_default_context(cafile=CA)
    for _ in range(n):
        req = urllib.request.Request(
            URL + "/api/pas/decide?item=1",
            data=json.dumps(request()).encode(),
            headers={"Authorization": "Bearer " + token, "Content-Type": "application/json",
                     "X-PAS-Request-Id": "demo-" + uuid.uuid4().hex[:12]},
            method="POST")
        with urllib.request.urlopen(req, context=ctx, timeout=20) as r:
            r.read()


if __name__ == "__main__":
    main()
