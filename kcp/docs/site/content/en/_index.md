---
title: kcp-cluster-api
description: Workspace-aware Cluster API for KCP
params:
  body_class: td-navbar-links-all-active
---

{{% blocks/cover title="kcp-cluster-api" height="full td-below-navbar" image_anchor="top" %}}

Making [Cluster API](https://cluster-api.sigs.k8s.io/) workspace-aware for
[KCP](https://github.com/kcp-dev/kcp).

<div class="td-cta-buttons my-5">
  <a class="btn btn-lg btn-primary me-3 mb-4" href="docs/user/">
    User docs
  </a>
  <a class="btn btn-lg btn-secondary me-3 mb-4" href="docs/design/">
    Design &amp; architecture
  </a>
  <a class="btn btn-lg btn-secondary mb-4"
    href="https://github.com/jimmidyson/kcp-cluster-api"
    target="_blank" rel="noopener noreferrer">
    Get the code <i class="fab fa-github ms-1"></i>
  </a>
</div>

{{% blocks/link-down color="info" %}}

{{% /blocks/cover %}}

{{% blocks/lead color="white" %}}

kcp-cluster-api is a fork of
[kubernetes-sigs/cluster-api](https://github.com/kubernetes-sigs/cluster-api)
that layers KCP (logical clusters / workspaces) support on top of unmodified
upstream Cluster API code, using upstream's existing extension points only.

{{% /blocks/lead %}}

{{% blocks/section color="primary" type="row" %}}

{{% blocks/feature title="User docs" icon="fa-book" url="docs/user/" %}}

Installing and running kcp-cluster-api: prerequisites, building the manager,
and day-to-day usage.

{{% /blocks/feature %}}

{{% blocks/feature title="Design &amp; architecture" icon="fa-diagram-project" url="docs/design/" %}}

Technical reference for developers and agents: the fork's invariants,
integration model, and deep dives into how it's built.

{{% /blocks/feature %}}

{{% blocks/feature title="Contributions welcome" icon="fab fa-github" url="https://github.com/jimmidyson/kcp-cluster-api" %}}

All new code, tests, and docs live under `kcp/` — see
[AGENTS.md](https://github.com/jimmidyson/kcp-cluster-api/blob/main/AGENTS.md)
for the rules governing contributions.

{{% /blocks/feature %}}

{{% /blocks/section %}}
