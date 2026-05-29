# Group Topology Affinity — API Design

| Item | Content |
|------|---------|
| Scope | PodGroup API additions for inter-group topology scheduling |
| Features | PodGroup anti-affinity, SubGroup affinity, SubGroup anti-affinity |
| Target file | `staging/src/volcano.sh/apis/pkg/apis/scheduling/v1beta1/types.go` |
| Related | [Network Topology Aware Scheduling](./Network%20Topology%20Aware%20Scheduling.md) |

## 1. Goals

Add a declarative way to express **inter-group** topology constraints on top of the
existing intra-group `NetworkTopologySpec`:

- **PodGroup anti-affinity** — spread multiple PodGroups (e.g. inference
  instances) across different topology domains for fault isolation.
- **SubGroup affinity** — colocate listed `subGroupPolicy` entries in one
  topology domain (e.g. prefill + decode on one supernode).
- **SubGroup anti-affinity** — keep listed `subGroupPolicy` entries (or shards
  split from one entry via `matchLabelKeys`) in different topology domains
  (e.g. each shard on its own rack).

## 2. Design principles (why this shape)

| Pain point in prior designs | This proposal |
|---|---|
| Two parallel top-level containers (`topologyAffinity` + `subGroupTopologyAffinity`) | **One** `topologyAffinity` field with three sub-blocks |
| `topologyDomain.topologyTierName` nested wrapper diverging from YAML | Put `topologyTier` / `topologyTierName` **directly on the term** (like K8s `topologyKey`) |
| `subGroupSelector` + `antiSubGroupSelector` (4 lines per term, subject/peer jargon) | A single `subGroups` list per term, with the **same shape** for affinity and anti-affinity |
| Separate `spread` / `separate` (or directional) forms for anti-affinity | **One rule**: a term's listed `subGroups` must occupy **distinct** domains (anti) or **one** domain (affinity) — see [§3.5](#35-the-unified-subgroups-rule) |
| `requiredDuringSchedulingIgnoredDuringExecution` verbosity | Short `required` / `preferred` keys, consistent with `NodeGroupAffinity` already in this file |
| `matchSubGroupPolicyNames: [prefill]` is wordy | Just `subGroups: [prefill]` |
| Easy to forget that label selectors match self | Scheduler auto-excludes the current PodGroup by UID; documented on every term |
| Reusing K8s `topologyKey` confuses Node-label vs HyperNode-tier semantics | Use `topologyTierName` / `topologyTier`, aligned with existing `NetworkTopologySpec` |

## 3. New types

### 3.1 Field added to `PodGroupSpec`

```go
// TopologyAffinity expresses inter-group topology relationships:
//   - Between this PodGroup and OTHER PodGroups (PodGroupAntiAffinity).
//   - Between SubGroups (subGroupPolicy entries) WITHIN this PodGroup
//     (SubGroupAffinity / SubGroupAntiAffinity).
// Tier semantics align with the cluster's HyperNode CRs
// (spec.tier / spec.tierName), the same source NetworkTopology uses.
// +optional
TopologyAffinity *TopologyAffinitySpec `json:"topologyAffinity,omitempty" protobuf:"bytes,7,opt,name=topologyAffinity"`
```

### 3.2 Container

```go
// TopologyAffinitySpec groups all inter-group topology constraints for a PodGroup.
type TopologyAffinitySpec struct {
    // PodGroupAntiAffinity forces this PodGroup to land in a DIFFERENT
    // topology domain than the matched peer PodGroups (typical use:
    // spread inference instances across supernodes for fault isolation).
    // +optional
    PodGroupAntiAffinity *PodGroupAntiAffinity `json:"podGroupAntiAffinity,omitempty" protobuf:"bytes,1,opt,name=podGroupAntiAffinity"`

    // SubGroupAffinity forces listed subGroupPolicy entries to share the
    // SAME topology domain (e.g. prefill+decode on one supernode).
    // +optional
    SubGroupAffinity *SubGroupAffinity `json:"subGroupAffinity,omitempty" protobuf:"bytes,2,opt,name=subGroupAffinity"`

    // SubGroupAntiAffinity forces listed subGroupPolicy entries (or shards
    // within one entry split by matchLabelKeys) to land in DIFFERENT
    // topology domains (e.g. each shard on its own rack).
    // +optional
    SubGroupAntiAffinity *SubGroupAntiAffinity `json:"subGroupAntiAffinity,omitempty" protobuf:"bytes,3,opt,name=subGroupAntiAffinity"`
}
```

### 3.3 Cross-PodGroup anti-affinity

```go
type PodGroupAntiAffinity struct {
    // Required: peer PodGroups MUST be in different topology domains.
    // If no domain satisfies the constraint, this PodGroup stays Pending.
    // +optional
    // +listType=atomic
    Required []PodGroupAffinityTerm `json:"required,omitempty" protobuf:"bytes,1,rep,name=required"`

    // Preferred: peer PodGroups SHOULD be in different topology domains;
    // scheduling still succeeds if the preference cannot be satisfied.
    // Each term may set Weight (1-100); higher = stronger preference.
    // +optional
    // +listType=atomic
    Preferred []PodGroupAffinityTerm `json:"preferred,omitempty" protobuf:"bytes,2,rep,name=preferred"`
}

// PodGroupAffinityTerm selects peer PodGroups and the tier at which their
// topology domain must differ from this one.
// The scheduler automatically EXCLUDES this PodGroup itself from peer matches.
type PodGroupAffinityTerm struct {
    // Weight applies only when this term appears under Preferred (1-100,
    // higher = stronger preference). Ignored under Required.
    // +kubebuilder:validation:Minimum=1
    // +kubebuilder:validation:Maximum=100
    // +optional
    Weight int32 `json:"weight,omitempty" protobuf:"bytes,5,opt,name=weight"`

    // PodGroupSelector matches other PodGroups by metadata.labels.
    // Standard metav1.LabelSelector semantics; an empty selector matches none
    // (use MatchLabels{} explicitly if you intend to match all peers).
    // +required
    PodGroupSelector *metav1.LabelSelector `json:"podGroupSelector" protobuf:"bytes,1,opt,name=podGroupSelector"`

    // NamespaceSelector limits peer PodGroups to matching namespaces.
    // nil = same namespace as this PodGroup. {} = all namespaces.
    // +optional
    NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty" protobuf:"bytes,2,opt,name=namespaceSelector"`

    // TopologyTierName: HyperNode.spec.tierName at which Domain_T is compared
    // (e.g. "supernode", "rack"). Mutually exclusive with TopologyTier.
    // +kubebuilder:validation:MaxLength=253
    // +optional
    TopologyTierName string `json:"topologyTierName,omitempty" protobuf:"bytes,3,opt,name=topologyTierName"`

    // TopologyTier: HyperNode.spec.tier integer at which Domain_T is compared.
    // Mutually exclusive with TopologyTierName. Exactly one of the two must be set.
    // +kubebuilder:validation:Minimum=0
    // +optional
    TopologyTier *int32 `json:"topologyTier,omitempty" protobuf:"bytes,4,opt,name=topologyTier"`
}
```

### 3.4 Same-PodGroup, cross-SubGroup

Affinity and anti-affinity share an **identical shape** — a list of `required`
and `preferred` terms, where each term carries a `subGroups` list and a tier.
Only the outcome differs (see [§3.5](#35-the-unified-subgroups-rule)).

```go
type SubGroupAffinity struct {
    // Required: the SubGroups in each term MUST share one domain.
    // +optional
    // +listType=atomic
    Required []SubGroupTerm `json:"required,omitempty" protobuf:"bytes,1,rep,name=required"`
    // Preferred: terms SHOULD be satisfied; may set Weight (1-100).
    // +optional
    // +listType=atomic
    Preferred []SubGroupTerm `json:"preferred,omitempty" protobuf:"bytes,2,rep,name=preferred"`
}

type SubGroupAntiAffinity struct {
    // Required: the SubGroups' SubJobs in each term MUST occupy distinct domains.
    // +optional
    // +listType=atomic
    Required []SubGroupTerm `json:"required,omitempty" protobuf:"bytes,1,rep,name=required"`
    // Preferred: terms SHOULD be satisfied; may set Weight (1-100).
    // +optional
    // +listType=atomic
    Preferred []SubGroupTerm `json:"preferred,omitempty" protobuf:"bytes,2,rep,name=preferred"`
}

// SubGroupTerm names a set of subGroupPolicy entries and the tier at which
// their topology domains are compared. The SAME type is used for both
// affinity (share one domain) and anti-affinity (occupy distinct domains);
// the meaning comes from whether the term sits under SubGroupAffinity or
// SubGroupAntiAffinity. See §3.5.
//
// Anti-affinity examples:
//   - subGroups: [prefill]            # prefill-0..N each in its own domain
//   - subGroups: [prefill, decode]    # all prefill AND decode SubJobs distinct
// Affinity example:
//   - subGroups: [prefill, decode]    # prefill + decode share one domain
type SubGroupTerm struct {
    // SubGroups names entries from spec.subGroupPolicy[].name.
    // +kubebuilder:validation:MinItems=1
    // +listType=atomic
    SubGroups []string `json:"subGroups" protobuf:"bytes,1,rep,name=subGroups"`

    // Weight applies only when this term appears under Preferred (1-100,
    // higher = stronger preference). Ignored under Required.
    // +kubebuilder:validation:Minimum=1
    // +kubebuilder:validation:Maximum=100
    // +optional
    Weight int32 `json:"weight,omitempty" protobuf:"bytes,2,opt,name=weight"`

    // TopologyTierName: HyperNode.spec.tierName at which domains are compared.
    // Mutually exclusive with TopologyTier.
    // +kubebuilder:validation:MaxLength=253
    // +optional
    TopologyTierName string `json:"topologyTierName,omitempty" protobuf:"bytes,3,opt,name=topologyTierName"`
    // TopologyTier: HyperNode.spec.tier at which domains are compared.
    // Mutually exclusive with TopologyTierName. Exactly one of the two must be set.
    // +kubebuilder:validation:Minimum=0
    // +optional
    TopologyTier *int32 `json:"topologyTier,omitempty" protobuf:"bytes,4,opt,name=topologyTier"`
}
```

### 3.5 The unified `subGroups` rule

A single sentence defines both fields:

> Within one term, all SubJobs of the listed `subGroups` must share **one**
> domain (`subGroupAffinity`) or occupy **distinct** domains
> (`subGroupAntiAffinity`), at the term's tier.

Everything users need follows from this one rule:

| What you want | Field | Term |
|---|---|---|
| prefill shards each on own rack | anti-affinity | `subGroups: [prefill]` @ rack |
| prefill shards AND decode shards each on own rack, roles may share | anti-affinity | two terms: `[prefill]`, `[decode]` @ rack |
| prefill and decode fully disjoint racks (no overlap) | anti-affinity | one term: `[prefill, decode]` @ rack |
| prefill + decode co-located on one supernode | affinity | `[prefill, decode]` @ supernode |

**Mixed relationships** (e.g. *prefill co-located with decode, but prefill
shards spread*) are expressed by combining one term in each field, at
different tiers — see [§4.5](#45-mixed-prefill-decode-affinity--prefill-shard-anti-affinity).
Affinity and anti-affinity on the same SubGroup **must** target different
tiers (the same domain cannot be both shared and distinct); the webhook
enforces affinity-tier ≥ anti-affinity-tier.

> **Note — pairwise correspondence is out of scope here.** "prefill-i
> co-located with decode-i while pairs spread" is a *grouping* concern, not a
> policy-level relationship: bind both roles on a shared `matchLabelKeys`
> (e.g. `volcano.sh/shard-id`) so each i-th pair forms one unit, then
> anti-affinity spreads the units. These fields intentionally do not model
> directional or index-paired edges.

## 4. User-facing YAML examples

### 4.1 Multi-instance fault isolation (PodGroup anti-affinity)

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: llama-70b-instance-0
  labels:
    topology.volcano.sh/group: llama-70b-prod
spec:
  minMember: 8
  queue: default
  topologyAffinity:
    podGroupAntiAffinity:
      required:
      - podGroupSelector:
          matchLabels:
            topology.volcano.sh/group: llama-70b-prod
        topologyTierName: supernode
```

### 4.2 Prefill–Decode: shards on separate racks, whole instance on one supernode

```yaml
spec:
  minMember: 44
  subGroupPolicy:
  - name: prefill
    labelSelector: { matchLabels: { volcano.sh/role: prefill } }
    matchLabelKeys: [volcano.sh/shard-id]
    subGroupSize: 8
    minSubGroups: 4
    networkTopology: { mode: hard, highestTierName: rack }
  - name: decode
    labelSelector: { matchLabels: { volcano.sh/role: decode } }
    matchLabelKeys: [volcano.sh/shard-id]
    subGroupSize: 6
    minSubGroups: 2
    networkTopology: { mode: hard, highestTierName: rack }
  topologyAffinity:
    subGroupAffinity:
      required:
      - subGroups: [prefill, decode]      # prefill + decode share one supernode
        topologyTierName: supernode
    subGroupAntiAffinity:
      required:
      - subGroups: [prefill]              # prefill shards each on own rack
        topologyTierName: rack
      - subGroups: [decode]              # decode shards each on own rack
        topologyTierName: rack
```

> Roles are co-located at the supernode tier but fault-isolated at the rack
> tier. Listing `[prefill]` and `[decode]` as **separate** terms spreads each
> role independently — a prefill and a decode shard may still share a rack.

### 4.3 Full cross-role rack disjointness

When prefill and decode must **never** share a rack, list them in **one**
anti-affinity term:

```yaml
topologyAffinity:
  subGroupAntiAffinity:
    required:
    - subGroups: [prefill, decode]   # all prefill AND decode SubJobs distinct
      topologyTierName: rack
```

### 4.4 Soft shard spread (resource-constrained clusters)

```yaml
topologyAffinity:
  subGroupAntiAffinity:
    preferred:
    - subGroups: [prefill]
      weight: 100
      topologyTierName: rack
```

### 4.5 Mixed: prefill–decode affinity + prefill shard anti-affinity

The user's "prefill affinity with decode, but prefill anti-affinity with
prefill" case. Polarity is already the top-level split, so a SubGroup may
appear in **both** fields — at **different tiers**:

```yaml
topologyAffinity:
  subGroupAffinity:
    required:
    - subGroups: [prefill, decode]   # prefill co-located with decode (supernode)
      topologyTierName: supernode
  subGroupAntiAffinity:
    required:
    - subGroups: [prefill]           # prefill shards spread (rack)
      topologyTierName: rack
```

> Valid because affinity (supernode) sits **above** anti-affinity (rack):
> prefill and decode share a supernode, while prefill shards occupy distinct
> racks inside it. A same-tier affinity + anti-affinity on `prefill` would
> be a genuine contradiction and is rejected by the webhook (rule 7).

## 5. Webhook validation rules

1. Each term: exactly one of `topologyTierName` / `topologyTier` is set.
2. `topologyTierName` must exist in cluster HyperNodes; `topologyTier` must
   match some `HyperNode.spec.tier`.
3. All names referenced by `subGroups` must appear in
   `spec.subGroupPolicy[].name`, and within a term must be distinct.
4. An **affinity** term's `subGroups` must have **≥ 2 distinct** names
   (co-locating one group with itself is meaningless).
5. An **anti-affinity** term allows **≥ 1** name. A single-name term requires
   that policy to declare `matchLabelKeys` (its shards are the spread unit) or
   `minSubGroups ≥ 2`.
6. `PodGroupAffinityTerm.podGroupSelector` is required; the scheduler
   auto-excludes the scheduling PodGroup by UID.
7. If the **same** SubGroup name appears in both a hard `subGroupAffinity`
   term and a hard `subGroupAntiAffinity` term, the affinity tier must be
   **strictly higher** than the anti-affinity tier (a domain cannot be both
   shared and distinct at the same tier). More generally, when both kinds are
   configured, affinity tier must be **≥** anti-affinity tier.
8. Reject the legacy `mode: hard|soft` field anywhere inside these terms
   (hard vs soft is expressed only via `required` vs `preferred`).
9. `weight` is only meaningful on terms under `preferred`; if set under
   `required` the webhook emits a warning and the scheduler ignores it.

## 6. User-friendliness comparison

| Scenario | This proposal | PR #5349 |
|---|---|---|
| Multi-instance spread | `podGroupAntiAffinity.required` (6 lines) | 9 lines (`requiredDuringScheduling…` + `topologyDomain` wrap) |
| Shard fault isolation (both roles) | two `subGroups` terms (6 lines) | ~14 lines (two dual-selector terms) |
| Cross-role disjointness | one `subGroups: [prefill, decode]` term (4 lines) | 8 lines (`subGroupSelector`/`antiSubGroupSelector`) |
| Soft preference | `subGroupAntiAffinity.preferred` term | 8 lines (`preferred…` + nested `term`) |

Key wins:

- **One concept, two polarities** — a single `subGroups` term shape means the
  same thing for affinity (share a domain) and anti-affinity (distinct
  domains); no subject/peer selector jargon, no directional `groupA`/`groupB`.
- **No hidden either/or rules** — there is exactly one field per term, so the
  webhook never has to police mutually exclusive sub-fields.
- **One container** (`topologyAffinity`) instead of two parallel ones.
- **No `topologyDomain` wrapper** — YAML structure matches the Go struct exactly.
- **Short list keys** `required` / `preferred`, matching the existing
  `NodeGroupAffinity` in this file.

## 7. Boundary with existing fields

| Field | Scope | Semantics |
|---|---|---|
| `spec.networkTopology` | Whole PodGroup | Intra-group envelope (do not cross tier) |
| `subGroupPolicy[].networkTopology` | One SubGroup | Intra-SubGroup Gang (do not cross tier) |
| `topologyAffinity.podGroupAntiAffinity` | This PodGroup ↔ other PodGroups | Different `Domain_T` at the chosen tier |
| `topologyAffinity.subGroupAffinity` | SubGroups within same PodGroup | Same `Domain_T` |
| `topologyAffinity.subGroupAntiAffinity` | SubGroups (or shards) within same PodGroup | Different `Domain_T` |

## 8. Out of scope (future work)

- Cross-PodGroup **affinity** (`podGroupAffinity`) — no concrete scenario yet;
  covered today by `networkTopology` or `subGroupAffinity` within one PodGroup.
- Cross-namespace SubGroup matching.
- `preempt` / `backfill` consistency with the inter-group occupancy index
  (Phase 2).
