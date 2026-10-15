#!/usr/bin/env python3
"""traffic.py N [FAXES] - sends N invented lumbar spine MRI requests to the public demonstration, as its PAS server
would, an invented appeal outcome for some denials, as the plan's appeals system would, and FAXES invented faxes (by
default, now and then one), as a fax service would.

Everything is invented: the patient is "Test Synthetic" with a made-up age, sex, race and ethnicity, and each request answers the policy's questions
at random (a question left unanswered is evidence nobody addressed, as in a real request). Standard library only.
Reads its bearer tokens from /opt/vaglio/conf (token, appeals-token, fax-token).
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
# Invented demographics, so the fairness report has groups to compare: US Core race and ethnicity (OMB categories).
RACES = [("2106-3", "White", 0.55), ("2054-5", "Black or African American", 0.2), ("2028-9", "Asian", 0.15),
         ("1002-5", "American Indian or Alaska Native", 0.05), ("2076-8", "Native Hawaiian or Other Pacific Islander", 0.05)]
ETHNICITIES = [("2135-2", "Hispanic or Latino", 0.25), ("2186-5", "Not Hispanic or Latino", 0.75)]
CORE = "http://hl7.org/fhir/us/core/StructureDefinition/"
OMB = "urn:oid:2.16.840.1.113883.6.238"


def pick(choices):
    r, total = random.random(), 0.0
    for code, name, weight in choices:
        total += weight
        if r < total:
            return code, name
    return choices[-1][:2]


def demographics(patient):
    patient["gender"] = random.choice(["female", "male", "female", "male", "unknown"])
    patient["birthDate"] = "%04d-%02d-%02d" % (random.randint(1940, 2004), random.randint(1, 12), random.randint(1, 28))
    ext = []
    if random.random() < 0.9:  # some requests do not say
        code, name = pick(RACES)
        ext.append({"url": CORE + "us-core-race", "extension": [
            {"url": "ombCategory", "valueCoding": {"system": OMB, "code": code, "display": name}},
            {"url": "text", "valueString": name}]})
    if random.random() < 0.9:
        code, name = pick(ETHNICITIES)
        ext.append({"url": CORE + "us-core-ethnicity", "extension": [
            {"url": "ombCategory", "valueCoding": {"system": OMB, "code": code, "display": name}},
            {"url": "text", "valueString": name}]})
    patient["extension"] = ext


def request():
    b = json.load(open(os.path.join(CONF, "template.json")))
    claim = b["entry"][0]["resource"]
    claim["identifier"] = [{"system": "http://provider.example/claims", "value": "DEMO-%06d" % random.randrange(10**6)}]
    claim["created"] = time.strftime("%Y-%m-%d", time.gmtime())
    if random.random() < 0.15:
        claim["priority"] = {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/processpriority", "code": "stat"}]}
    b["entry"][1]["resource"]["address"] = [{"use": "home", "state": random.choice(STATES)}]
    demographics(b["entry"][1]["resource"])
    items = []
    for link, text, yes in QUESTIONS:
        if random.random() < 0.15:
            continue  # not addressed
        items.append({"linkId": link, "text": text, "answer": [{"valueBoolean": random.random() < yes}]})
    qr = b["entry"][4]["resource"]
    qr["item"] = items
    return b


def secret(name):
    try:
        return open(os.path.join(CONF, name)).read().strip()
    except OSError:
        return ""


def post(ctx, path, token, body, content_type="application/json"):
    req = urllib.request.Request(URL + path, data=body, method="POST",
                                 headers={"Authorization": "Bearer " + token, "Content-Type": content_type,
                                          "X-PAS-Request-Id": "demo-" + uuid.uuid4().hex[:12]})
    with urllib.request.urlopen(req, context=ctx, timeout=20) as r:
        return r.read()


def appeal(ctx, case):
    """An invented appeal of a denial, decided in the plan's appeals system: some overturned, more upheld."""
    token = secret("appeals-token")
    if not token or random.random() > 0.4:
        return
    overturned = random.random() < 0.35
    outcome = [{"case": case, "reference": "DEMO-APL-" + uuid.uuid4().hex[:10],
                "outcome": "overturned" if overturned else "upheld",
                "reason": "Invented: records sent on appeal showed six weeks of therapy" if overturned
                else "Invented: the appeal added nothing the policy reads",
                "decided": time.strftime("%Y-%m-%d", time.gmtime()), "by": "demo appeals desk"}]
    post(ctx, "/api/feeds/appeals", token, json.dumps(outcome).encode())


def pdf(lines):
    """A one-page PDF of plain text, written by hand: no library needed."""
    text = "BT /F1 12 Tf 72 740 Td 16 TL " + " ".join(
        "(%s) '" % l.replace("\\", "\\\\").replace("(", "\\(").replace(")", "\\)") for l in lines) + " ET"
    objs = ["<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
            "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R "
            "/Resources << /Font << /F1 5 0 R >> >> >>",
            "<< /Length %d >>\nstream\n%s\nendstream" % (len(text), text),
            "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"]
    out, offsets = b"%PDF-1.4\n", []
    for i, o in enumerate(objs, 1):
        offsets.append(len(out))
        out += ("%d 0 obj\n%s\nendobj\n" % (i, o)).encode()
    xref = len(out)
    out += ("xref\n0 %d\n0000000000 65535 f \n" % (len(objs) + 1)).encode()
    out += "".join("%010d 00000 n \n" % o for o in offsets).encode()
    out += ("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (len(objs) + 1, xref)).encode()
    return out


def fax(ctx):
    """An invented faxed request, as a fax service would post it."""
    token = secret("fax-token")
    if not token:
        return
    lines = ["PRIOR AUTHORIZATION REQUEST - INVENTED FOR THE VAGLIO DEMONSTRATION",
             "Date: " + time.strftime("%Y-%m-%d", time.gmtime()),
             "Patient: Test Synthetic, born 1970-01-01 (not a real person)",
             "Service: MRI lumbar spine without contrast, CPT 72148",
             "Ordering provider: Example Clinic, NPI 1234567893", ""]
    for link, text, yes in QUESTIONS:
        if random.random() < 0.2:
            continue
        lines.append("%s %s" % (text, "Yes" if random.random() < yes else "No"))
    q = "?pages=1&from=%2B1555010" + "%04d" % random.randrange(10**4)
    post(ctx, "/api/feeds/fax" + q, token, pdf(lines), "application/pdf")


def main():
    n = int(sys.argv[1]) if len(sys.argv) > 1 else 1
    faxes = int(sys.argv[2]) if len(sys.argv) > 2 else (1 if random.random() < 0.2 else 0)
    token = secret("token")
    ctx = ssl.create_default_context(cafile=CA)
    for _ in range(n):
        answer = json.loads(post(ctx, "/api/pas/decide?item=1", token, json.dumps(request()).encode()))
        if answer.get("decision") == "deny" and answer.get("case"):
            appeal(ctx, answer["case"])
    for _ in range(faxes):
        fax(ctx)


if __name__ == "__main__":
    main()
