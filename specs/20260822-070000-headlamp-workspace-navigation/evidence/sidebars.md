# What the UI showed, workspace by workspace

Driven with a browser against a live `task demo --wait` run: Headlamp
0.45.0 (built from `headlamp-helm-0.45.0`), serving the kubeconfig the
run wrote, with the Cluster API plugin and the kcp workspaces plugin
loaded. Each row is what the sidebar contained in that workspace.

| Workspace | Cluster API section | Pods | Workloads | Sidebar |
|---|---|---|---|---|
| `root` | no | no | no | Home, Clusters, root, Namespaces, Advanced Search (Beta), Map, Security, Configuration, Custom Resources, Workspaces |
| `root:capi-demo` | no | no | no | Home, Clusters, root:capi-demo, Namespaces, Advanced Search (Beta), Map, Security, Configuration, Custom Resources, Workspaces |
| `root:capi-demo:alice` | no | no | no | Home, Clusters, root:capi-demo:alice, Namespaces, Advanced Search (Beta), Map, Security, Configuration, Custom Resources, Workspaces |
| `root:capi-demo:alice:capi-demo-1` | yes | no | no | Home, Clusters, root:capi-demo:alice:capi-demo-1, Namespaces, Advanced Search (Beta), Map, Security, Configuration, Custom Resources, Cluster API, Workspaces |

The second column is the claim: the section follows the `APIBinding`,
not the deployment. The third and fourth are the other half of it - no
workspace serves Pods, so no workspace offers them.

`evidence/navigator.png` is the navigator in `root:capi-demo:alice`,
which says `capi-demo-1` lights up Cluster API before it is opened.
`evidence/cluster-api-in-a-bound-workspace.png` is that workspace's
Cluster API overview, reading the demo's cluster through an `APIBinding`
with no `CustomResourceDefinition` anywhere in the workspace.
## Reaching a workspace nobody configured

A second Headlamp, given only the `root` context on its backend and alice's
home workspace as a kubeconfig held in the browser:

| Step | Result |
|---|---|
| Navigator in `root:capi-demo:alice` | lists `capi-demo-1`, type `cluster-api`, plugins column `no context` |
| Its **Open** button | enabled, because Headlamp holds a kubeconfig to copy |
| Clicking it | lands on `/c/root:capi-demo:alice:capi-demo-1/`, a cluster that did not exist a moment earlier |
| The sidebar there | has the Cluster API section |

Where the credentials live on the backend instead - which is how the demo runs
- there is nothing for a plugin to copy, the button says so, and the contexts
the run wrote are what makes the tree navigable.
