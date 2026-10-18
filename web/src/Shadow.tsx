import { useState } from "react";
import { errorText, when } from "./common";
import { useMeQuery, useRuleMutation, useShadowReportQuery } from "./store";
import type { CaseDetail, Me } from "./types";

const word: Record<string, string> = {
  approve: "approved",
  deny: "denied",
  pend: "pended (asked for evidence)",
};
const supportsText: Record<string, string> = {
  vaglio: "Vaglio's decision",
  plan: "the plan's decision",
  both: "both are defensible",
  neither: "neither",
};
const pc = (v: number) => `${(v * 100).toFixed(1)}%`;

/** The shadow pilot's report: agreement with the plan, and the disagreements left for a clinician. */
export function ShadowPilot() {
  const { data: me } = useMeQuery();
  const { data: r, error } = useShadowReportQuery(undefined, {
    skip: !me?.shadow,
  });
  if (!me?.shadow) return null;
  if (error)
    return (
      <p role="alert">
        {errorText(error, "Could not make the shadow pilot report.")}
      </p>
    );
  if (!r) return <p>Loading…</p>;
  return (
    <>
      <h3>Shadow pilot: Vaglio beside the plan's decisions</h3>
      <p className="meta">
        Every request was decided here and given to no one; the plan's own
        process answered it. {r.cases} decided, {r.withOutcome} with the plan's
        decision known.
      </p>
      <div
        className="mb-5 grid gap-3 sm:grid-cols-2 lg:grid-cols-4"
        aria-label="The pilot at a glance"
      >
        {[
          {
            label: "Agreed where both decided",
            value: r.bothDecided ? pc(r.agreement) : "n/a",
            note: r.bothDecided
              ? `${r.agreed} of ${r.bothDecided}; 95%: ${pc(r.interval[0])}–${pc(r.interval[1])}`
              : "no outcomes yet",
            text: "text-ok",
          },
          {
            label: "Cohen's kappa",
            value: r.bothDecided ? r.kappa.toFixed(2) : "n/a",
            note: "agreement beyond chance; 1 is perfect",
            text: "text-info",
          },
          {
            label: "Vaglio asked for more",
            value: String(r.vaglioPended),
            note: "evidence the record lacked",
            text: "text-due",
          },
          {
            label: "Awaiting a clinician",
            value: String(r.unruled),
            note: "disagreements not ruled on",
            text: "text-bad",
          },
        ].map((k) => (
          <div
            key={k.label}
            className="rounded-xl border border-line bg-raised p-4 shadow-sm"
          >
            <span className="text-[11px] font-bold uppercase tracking-[0.1em] text-faint">
              {k.label}
            </span>
            <div className={`mt-2 text-3xl font-bold ${k.text}`}>{k.value}</div>
            <div className="text-xs text-muted">{k.note}</div>
          </div>
        ))}
      </div>
      <table className="cases">
        <caption className="text-left font-semibold">
          Vaglio's verdict by the plan's
        </caption>
        <thead>
          <tr>
            <th scope="col">Vaglio</th>
            <th scope="col">The plan approved</th>
            <th scope="col">The plan denied</th>
          </tr>
        </thead>
        <tbody>
          {["approve", "deny", "pend"].map((v) => (
            <tr key={v}>
              <th scope="row">{word[v]}</th>
              <td>{r.grid[v]?.approve ?? 0}</td>
              <td>{r.grid[v]?.deny ?? 0}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {r.overturned > 0 && (
        <p>
          {r.overturned} of the plan's denials that Vaglio would have approved
          were overturned on appeal.
        </p>
      )}
      {r.byPolicy.length > 0 && (
        <table className="cases">
          <caption className="text-left font-semibold">By policy</caption>
          <thead>
            <tr>
              <th scope="col">Policy</th>
              <th scope="col">Both decided</th>
              <th scope="col">Agreed</th>
              <th scope="col">Vaglio pended</th>
            </tr>
          </thead>
          <tbody>
            {r.byPolicy.map((g) => (
              <tr key={g.key}>
                <td>{g.title || g.key}</td>
                <td>{g.bothDecided}</td>
                <td>
                  {g.bothDecided ? `${g.agreed} (${pc(g.agreement)})` : "–"}
                </td>
                <td>{g.pended}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {!!r.disagreements?.length && (
        <table className="cases">
          <caption className="text-left font-semibold">
            Disagreements, those awaiting a ruling first{" "}
            <a
              href="/api/reports/shadow?format=csv"
              className="text-xs font-normal"
            >
              CSV
            </a>
          </caption>
          <thead>
            <tr>
              <th scope="col">Request</th>
              <th scope="col">Vaglio</th>
              <th scope="col">The plan</th>
              <th scope="col">A clinician found</th>
            </tr>
          </thead>
          <tbody>
            {r.disagreements.map((d) => (
              <tr key={d.case}>
                <td>
                  <a href={`#/cases/${d.case}`}>{d.claim}</a>{" "}
                  <span className="meta">
                    item {d.item}, {when(d.created)}
                  </span>
                </td>
                <td className={d.vaglio === "approve" ? "text-ok" : "text-bad"}>
                  {word[d.vaglio]}
                </td>
                <td>
                  {word[d.plan]}
                  {d.by && <span className="meta"> by {d.by}</span>}
                  {d.appeal && (
                    <span className="meta">, {d.appeal} on appeal</span>
                  )}
                </td>
                <td>
                  {d.ruling ? (
                    `${supportsText[d.ruling.supports]} (${d.ruling.by})`
                  ) : (
                    <span className="meta">not yet ruled</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <p className="meta">{r.note}</p>
    </>
  );
}

/** On a shadow case: what the plan decided, and a clinician's ruling where the two disagree. */
export function PlanDecision({ c, me }: { c: CaseDetail; me: Me }) {
  const [supports, setSupports] = useState("");
  const [note, setNote] = useState("");
  const [rule, { isLoading, error }] = useRuleMutation();
  if (!me.shadow) return null;
  const o = c.planOutcome;
  const verdict =
    c.status === "reviewed" && c.review
      ? c.review.decision
      : c.proposed && c.status === "pended"
        ? "deny"
        : c.answer.decision;
  const disagree =
    !!o &&
    (verdict === "approve" || verdict === "deny") &&
    verdict !== o.decision;
  const clinician = !!me.npi && !!me.credential;
  return (
    <>
      <h3>The plan's own decision</h3>
      {!o ? (
        <p className="meta">
          Not received yet: it comes from the plan's systems on the outcomes
          feed.
        </p>
      ) : (
        <p>
          The plan {word[o.decision]} it{o.by ? `, by ${o.by}` : ""}
          {o.decided ? ` on ${o.decided}` : ""}
          {o.appeal ? `, and the denial was ${o.appeal} on appeal` : ""}.{" "}
          {disagree ? (
            <strong className="text-due">
              Vaglio would have {word[verdict]} it.
            </strong>
          ) : verdict === "pend" ? (
            <span>Vaglio would have asked for evidence instead.</span>
          ) : (
            <span className="text-ok">Vaglio agrees.</span>
          )}
        </p>
      )}
      {c.ruling && (
        <p>
          A clinician ({c.ruling.by}
          {c.ruling.credential ? `, ${c.ruling.credential}` : ""}) found the
          record supports <strong>{supportsText[c.ruling.supports]}</strong>:{" "}
          {c.ruling.note} <span className="meta">{when(c.ruling.time)}</span>
        </p>
      )}
      {disagree && !c.ruling && clinician && (
        <form
          className="review"
          onSubmit={(e) => {
            e.preventDefault();
            void rule({ id: c.id, supports, note });
          }}
        >
          <fieldset>
            <legend>Which decision does the record support?</legend>
            {(["vaglio", "plan", "both", "neither"] as const).map((k) => (
              <label key={k} className="mr-4">
                <input
                  type="radio"
                  name="supports"
                  value={k}
                  checked={supports === k}
                  onChange={() => setSupports(k)}
                />{" "}
                {supportsText[k]}
              </label>
            ))}
          </fieldset>
          <label htmlFor="ruling-note">What in the record supports it</label>
          <textarea
            id="ruling-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={3}
          />
          <button
            type="submit"
            disabled={!supports || !note.trim() || isLoading}
          >
            Record the ruling
          </button>
          {error && (
            <p role="alert">
              {errorText(error, "Could not record the ruling.")}
            </p>
          )}
        </form>
      )}
      {disagree && !c.ruling && !clinician && (
        <p className="meta">A clinician rules on this disagreement.</p>
      )}
    </>
  );
}
