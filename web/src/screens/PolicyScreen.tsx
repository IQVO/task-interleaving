import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Card } from "@warehouse/ui-kit";
import { ApiError, declareSitePolicy, getSitePolicy, toApiError } from "../api";
import { Form, FormRow, InlineError, InlineSuccess, SubmitButton, TextField } from "../components/formkit";
import { Facts, PageHeader, Stack } from "../components/layout";
import { ProblemAlert, RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { idProblem, utcLabel } from "../lib";
import { draftOf, EMPTY_DRAFT, validatePolicy } from "../policyForm";
import type { PolicyDraft } from "../policyForm";
import { TASK_TYPES } from "../types";
import type { SitePolicy, TaskType } from "../types";

/** `policy`: a lookup by site id, then `policy/:siteId`. */
export function PolicyLookupScreen() {
  const [siteId, setSiteId] = useState("");
  const navigate = useNavigate();
  const id = siteId.trim();
  const problem = id === "" ? null : idProblem("site", id);
  const go = () => {
    if (id !== "" && problem === null) navigate(encodeURIComponent(id));
  };
  return (
    <Stack gap={5}>
      <PageHeader title="Site policy" subtitle="Interleaving is advice a site opts into: a site without a policy is inert" />
      <Card title="Open a site's policy">
        <Form label="Site lookup" onSubmit={go}>
          <FormRow>
            <TextField
              label="Site"
              value={siteId}
              onChange={setSiteId}
              placeholder="SITE-1"
              hint={problem ?? "View it, change it, or declare it for the first time"}
            />
            <SubmitButton disabled={id === "" || problem !== null}>Open policy</SubmitButton>
          </FormRow>
        </Form>
      </Card>
    </Stack>
  );
}

/** The route element of `policy/:siteId`. */
export function PolicyRoute() {
  const { siteId } = useParams();
  return siteId ? <PolicyScreen siteId={siteId} /> : null;
}

/**
 * A site's policy: GET /sites/{siteId}/policy and the edit that PUTs it back.
 * The edit sends `If-Match` with the version it was loaded at, so a policy
 * changed by someone else meanwhile is a 412 (stale) instead of a silent
 * overwrite; the screen then offers to reload. A site with no policy (404
 * policy-not-found) shows the form empty: the first declaration sends no
 * If-Match.
 */
export function PolicyScreen({ siteId }: { siteId: string }) {
  const policy = useRequest(() => getSitePolicy(siteId), `policy|${siteId}`);
  // Kept here, not in the editor: saving reloads the policy, which remounts it.
  const [savedVersion, setSavedVersion] = useState<number | null>(null);
  const saved = (version: number) => {
    setSavedVersion(version);
    policy.reload();
  };
  const notDeclared = policy.status === "error" && policy.error.slug === "policy-not-found";
  return (
    <Stack gap={5}>
      <PageHeader title={`Policy of ${siteId}`} subtitle="How far, how many and how long: the settings every hint of this site is issued under" />
      <InlineSuccess message={savedVersion === null ? null : `Policy saved as version ${savedVersion}.`} />
      {notDeclared ? (
        <>
          <Card title="No policy declared">
            This site is inert: no hint is issued for it until a policy is declared.
          </Card>
          <PolicyEditor key="new" siteId={siteId} current={undefined} onSaved={saved} />
        </>
      ) : (
        <RequestView state={policy} what="the policy">
          {(p) => (
            <>
              <PolicyFacts policy={p} />
              <PolicyEditor key={p.version} siteId={siteId} current={p} onSaved={saved} onStale={policy.reload} />
            </>
          )}
        </RequestView>
      )}
    </Stack>
  );
}

function PolicyFacts({ policy }: { policy: SitePolicy }) {
  return (
    <Card title="Current policy">
      <Facts
        items={[
          { label: "Max detour", value: `${policy.maxDetourM} m` },
          { label: "Max candidates", value: String(policy.maxCandidates) },
          { label: "Hint TTL", value: `${policy.hintTtlSeconds} s` },
          { label: "Eligible task types", value: policy.eligibleTaskTypes.join(", ") },
          { label: "Vertical weight", value: String(policy.verticalWeight) },
          { label: "Version", value: String(policy.version) },
          { label: "Declared", value: utcLabel(policy.declaredAt) },
        ]}
      />
    </Card>
  );
}

function PolicyEditor({
  siteId,
  current,
  onSaved,
  onStale,
}: {
  siteId: string;
  current: SitePolicy | undefined;
  onSaved: (version: number) => void;
  onStale?: () => void;
}) {
  const [draft, setDraft] = useState<PolicyDraft>(current ? draftOf(current) : EMPTY_DRAFT);
  const [problems, setProblems] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const toggle = (t: TaskType) =>
    setDraft({
      ...draft,
      eligibleTaskTypes: draft.eligibleTaskTypes.includes(t)
        ? draft.eligibleTaskTypes.filter((x) => x !== t)
        : [...draft.eligibleTaskTypes, t],
    });

  const save = () => {
    const v = validatePolicy(draft);
    setError(null);
    if (!v.ok) {
      setProblems(v.problems);
      return;
    }
    setProblems([]);
    setSaving(true);
    declareSitePolicy(siteId, v.settings, current?.version).then(
      (p) => {
        setSaving(false);
        onSaved(p.version);
      },
      (err: unknown) => {
        setSaving(false);
        setError(toApiError(err));
      },
    );
  };

  const stale = error !== null && (error.slug === "version-mismatch" || error.slug === "concurrent-modification");
  return (
    <Card title={current ? `Change the policy (editing version ${current.version})` : "Declare the policy"}>
      <Form label="Policy form" onSubmit={save}>
        <FormRow>
          <TextField label="Max detour (m)" required value={draft.maxDetourM} onChange={(v) => setDraft({ ...draft, maxDetourM: v })} inputMode="text" />
          <TextField label="Max candidates (1 to 5)" required value={draft.maxCandidates} onChange={(v) => setDraft({ ...draft, maxCandidates: v })} inputMode="numeric" />
          <TextField label="Hint TTL (seconds)" required value={draft.hintTtlSeconds} onChange={(v) => setDraft({ ...draft, hintTtlSeconds: v })} inputMode="numeric" />
          <TextField label="Vertical weight" required value={draft.verticalWeight} onChange={(v) => setDraft({ ...draft, verticalWeight: v })} inputMode="text" hint="Multiplies the vertical distance; 1 counts it like the horizontal" />
        </FormRow>
        <fieldset style={{ border: "1px solid var(--wh-color-border)", borderRadius: "var(--wh-radius-md)", padding: "var(--wh-space-3)" }}>
          <legend style={{ fontSize: "var(--wh-font-size-xs)", color: "var(--wh-color-text-muted)", fontWeight: 600 }}>Eligible task types *</legend>
          <div style={{ display: "flex", gap: "var(--wh-space-4)", flexWrap: "wrap" }}>
            {TASK_TYPES.map((t) => (
              <label key={t} style={{ display: "flex", gap: 6, alignItems: "center", fontSize: "var(--wh-font-size-sm)" }}>
                <input type="checkbox" checked={draft.eligibleTaskTypes.includes(t)} onChange={() => toggle(t)} />
                {t}
              </label>
            ))}
          </div>
        </fieldset>
        {problems.length > 0 && <InlineError message={problems.join(" ")} />}
        {error && <ProblemAlert error={error} lead={stale ? "Someone changed this policy since you opened it." : undefined} />}
        {stale && onStale && (
          <div>
            <SubmitButton type="button" secondary onClick={onStale}>
              Reload the current policy
            </SubmitButton>
          </div>
        )}
        <div>
          <SubmitButton disabled={saving}>{current ? "Save new version" : "Declare policy"}</SubmitButton>
        </div>
      </Form>
    </Card>
  );
}
