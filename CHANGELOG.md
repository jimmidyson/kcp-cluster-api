# Changelog

## 0.2.0 (2026-08-22)

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* feat: browse the demo's workspaces in a UI by @jimmidyson in https://github.com/jimmidyson/kcp-cluster-api/pull/106
* fix: detect a signature by reading it, not by verifying it by @jimmidyson in https://github.com/jimmidyson/kcp-cluster-api/pull/105
* ci: take release notes from GitHub's generator, labelled from commit titles by @jimmidyson in https://github.com/jimmidyson/kcp-cluster-api/pull/104
* docs: mark the UI work shipped, and correct what the plan says it does by @jimmidyson in https://github.com/jimmidyson/kcp-cluster-api/pull/108


**Full Changelog**: https://github.com/jimmidyson/kcp-cluster-api/compare/v0.1.0...v0.2.0

## 0.1.0 (2026-08-22)


### ⚠ BREAKING CHANGES

* require a control plane, and publish every export unconditionally ([#87](https://github.com/jimmidyson/kcp-cluster-api/issues/87))
* base every cluster on a ClusterClass ([#80](https://github.com/jimmidyson/kcp-cluster-api/issues/80))
* give every provider its own APIExport and its own deployment ([#39](https://github.com/jimmidyson/kcp-cluster-api/issues/39))
* serve every workspace from one set of fleet-wide controllers ([#33](https://github.com/jimmidyson/kcp-cluster-api/issues/33))
* --workspace-cluster-name is removed. Every workspace bound to the export is reconciled without being named anywhere. The one workspace whose admission and conversion webhooks are served, if any, is named by the new --webhook-workspace-cluster-name; left unset, no webhooks are served and reconciliation is unaffected.
* invert repository to a standalone module with task runner and CI ([#22](https://github.com/jimmidyson/kcp-cluster-api/issues/22))

### Features

* base every cluster on a ClusterClass ([#80](https://github.com/jimmidyson/kcp-cluster-api/issues/80)) ([6cd792d](https://github.com/jimmidyson/kcp-cluster-api/commit/6cd792d737bbe2d14fc6f5e160032cd561f23ae7))
* build a cluster by default in the demo ([#58](https://github.com/jimmidyson/kcp-cluster-api/issues/58)) ([35f3189](https://github.com/jimmidyson/kcp-cluster-api/commit/35f3189a7ed6bbfc4d41aae814859812c51b0416))
* claim only the verbs each provider's own RBAC justifies ([#44](https://github.com/jimmidyson/kcp-cluster-api/issues/44)) ([6cde4a6](https://github.com/jimmidyson/kcp-cluster-api/commit/6cde4a61ff9d15e5ff0f68627cd0f8062287a771))
* derive core's permission claims from the providers installed ([#89](https://github.com/jimmidyson/kcp-cluster-api/issues/89)) ([65935bd](https://github.com/jimmidyson/kcp-cluster-api/commit/65935bdc7787ef4d977d751fe510928cc5d09d7a))
* discover a workspace by its LogicalCluster, and measure the cost ([#96](https://github.com/jimmidyson/kcp-cluster-api/issues/96)) ([78fe6e7](https://github.com/jimmidyson/kcp-cluster-api/commit/78fe6e726a0e35f0defa3fcec8a157d517136975))
* give every cluster a worker pool and a kubeconfig ([#40](https://github.com/jimmidyson/kcp-cluster-api/issues/40)) ([79cff37](https://github.com/jimmidyson/kcp-cluster-api/commit/79cff370d19d40d493b1496de8539eb03988ceb8))
* give every provider its own APIExport and its own deployment ([#39](https://github.com/jimmidyson/kcp-cluster-api/issues/39)) ([36aed9f](https://github.com/jimmidyson/kcp-cluster-api/commit/36aed9f63f9f958304a08941bed921dbb19454f7))
* give the demo two users and show neither can see the other's workspaces ([#77](https://github.com/jimmidyson/kcp-cluster-api/issues/77)) ([2a1083d](https://github.com/jimmidyson/kcp-cluster-api/commit/2a1083d71969bb46ccaca5e5ea1124b65df1947b))
* invert repository to a standalone module with task runner and CI ([#22](https://github.com/jimmidyson/kcp-cluster-api/issues/22)) ([3f7a101](https://github.com/jimmidyson/kcp-cluster-api/commit/3f7a1010e5893bfcf110f2528609b73c2aea4cf1))
* let a tenant enable a provider, and show it in the demo ([#91](https://github.com/jimmidyson/kcp-cluster-api/issues/91)) ([da24b43](https://github.com/jimmidyson/kcp-cluster-api/commit/da24b43fbcee8c9527f41b791341dcbf6e694e91))
* measure per-workspace resource usage with active workspace sweeps ([#26](https://github.com/jimmidyson/kcp-cluster-api/issues/26)) ([e89420f](https://github.com/jimmidyson/kcp-cluster-api/commit/e89420fd35ffa95b33b0542ddd17b78bffaadd41))
* measure what a workspace costs, with one instrument ([#32](https://github.com/jimmidyson/kcp-cluster-api/issues/32)) ([88447ca](https://github.com/jimmidyson/kcp-cluster-api/commit/88447cad03e77982243e5fce3a9d64d9f9c0dc3c))
* narrow the tenant role to one writable type ([#83](https://github.com/jimmidyson/kcp-cluster-api/issues/83)) ([88c3d0e](https://github.com/jimmidyson/kcp-cluster-api/commit/88c3d0e121ca4aabef2baa8e719d114dc11af242))
* onboard a workspace to Cluster API with a WorkspaceType ([#90](https://github.com/jimmidyson/kcp-cluster-api/issues/90)) ([8a51e5c](https://github.com/jimmidyson/kcp-cluster-api/commit/8a51e5c339c92e3dd9be9735a7c2e6a05f1b27a2))
* provision clusters across many workspaces in one command ([#37](https://github.com/jimmidyson/kcp-cluster-api/issues/37)) ([9e4b8e0](https://github.com/jimmidyson/kcp-cluster-api/commit/9e4b8e02ab1fbd6aa1a86f11ecdd2b623dafeb53))
* reconcile every workspace bound to the APIExport ([#25](https://github.com/jimmidyson/kcp-cluster-api/issues/25)) ([788cc74](https://github.com/jimmidyson/kcp-cluster-api/commit/788cc7424951c663f7aeb6d55f2e0d1edd973ba1))
* run the kubeadm bootstrap provider across every workspace ([#38](https://github.com/jimmidyson/kcp-cluster-api/issues/38)) ([d83595d](https://github.com/jimmidyson/kcp-cluster-api/commit/d83595d64242a603d08eba1da672b7f610604904))
* run the Nutanix infrastructure provider across every workspace ([#86](https://github.com/jimmidyson/kcp-cluster-api/issues/86)) ([5b1bf35](https://github.com/jimmidyson/kcp-cluster-api/commit/5b1bf359fd002f4f676732d79d799eb4dad48884))
* serve every workspace from one set of fleet-wide controllers ([#33](https://github.com/jimmidyson/kcp-cluster-api/issues/33)) ([428c83f](https://github.com/jimmidyson/kcp-cluster-api/commit/428c83f3005e0a37f297a10bba603de0170c19b8))
* wait for ready clusters, not provisioned ones ([#57](https://github.com/jimmidyson/kcp-cluster-api/issues/57)) ([91dcf12](https://github.com/jimmidyson/kcp-cluster-api/commit/91dcf12c964b3c812bfb0bd38fdaa29db7369676))


### Bug Fixes

* call a CNI installed when its pods run, not when the manifest applies ([#88](https://github.com/jimmidyson/kcp-cluster-api/issues/88)) ([5d49a21](https://github.com/jimmidyson/kcp-cluster-api/commit/5d49a215d35a06cc6e16e6052ee91a300cb752fd))
* connect to the container runtime instead of looking for its socket ([#61](https://github.com/jimmidyson/kcp-cluster-api/issues/61)) ([5672ba6](https://github.com/jimmidyson/kcp-cluster-api/commit/5672ba6267bb756d820afed7db818d243f42a3c8))
* give a docker-backed demo cluster a control plane port ([#65](https://github.com/jimmidyson/kcp-cluster-api/issues/65)) ([776f171](https://github.com/jimmidyson/kcp-cluster-api/commit/776f171e1c105e2d20ffc99c2845f8017c877421))
* index Nodes by provider ID on the fleet-wide ClusterCache ([#56](https://github.com/jimmidyson/kcp-cluster-api/issues/56)) ([94121a6](https://github.com/jimmidyson/kcp-cluster-api/commit/94121a6cccd4140a0d64f532825f603750885493))
* let a workspace unbind while it still holds clusters ([#42](https://github.com/jimmidyson/kcp-cluster-api/issues/42)) ([1f125e2](https://github.com/jimmidyson/kcp-cluster-api/commit/1f125e2d594be57a4987e2e2ef26da8e5da41103))
* read owner/repo from any remote, not only one naming github.com ([#98](https://github.com/jimmidyson/kcp-cluster-api/issues/98)) ([b63446f](https://github.com/jimmidyson/kcp-cluster-api/commit/b63446f42c9a3d90cf53c92f8b81edd9cd8969af))
* require a control plane, and publish every export unconditionally ([#87](https://github.com/jimmidyson/kcp-cluster-api/issues/87)) ([2dfbba9](https://github.com/jimmidyson/kcp-cluster-api/commit/2dfbba9858dd2ed659501dd6f43d6226995b9e88))
* start at 0.1.0, and give release pull requests a release: prefix ([#102](https://github.com/jimmidyson/kcp-cluster-api/issues/102)) ([22ca5ce](https://github.com/jimmidyson/kcp-cluster-api/commit/22ca5ce06fe3fbb6b4a9a10d950b4829f115e35a))
* stop a stalled ClusterCache retaining a departed workspace's caches ([#45](https://github.com/jimmidyson/kcp-cluster-api/issues/45)) ([7af9ffc](https://github.com/jimmidyson/kcp-cluster-api/commit/7af9ffccc545afbb919fd11ddfeb2f2c88bb7369))


### Refactoring

* express APIExports and APIBindings in kcp's v1alpha2 shape ([#43](https://github.com/jimmidyson/kcp-cluster-api/issues/43)) ([5dac243](https://github.com/jimmidyson/kcp-cluster-api/commit/5dac2430be5b4f86e850fd6f9f8cf0b261e45167))


### Documentation

* add a walkthrough that explains kcp while taking the demo apart ([#70](https://github.com/jimmidyson/kcp-cluster-api/issues/70)) ([401db92](https://github.com/jimmidyson/kcp-cluster-api/commit/401db92f85fb6f0e2063bbb06adc79a9bf058b11))
* add NAMESPACE column to cluster and machine queries ([#82](https://github.com/jimmidyson/kcp-cluster-api/issues/82)) ([c0981d9](https://github.com/jimmidyson/kcp-cluster-api/commit/c0981d957e75dc098efe240d119a61d4b4b8d3a6))
* bring the conversion plan back in line with what shipped ([#84](https://github.com/jimmidyson/kcp-cluster-api/issues/84)) ([6192bd5](https://github.com/jimmidyson/kcp-cluster-api/commit/6192bd51b0890e0c548d1edf1a7dbe047b629a86))
* decide how Cluster API becomes workspace-aware ([#31](https://github.com/jimmidyson/kcp-cluster-api/issues/31)) ([b0fcd54](https://github.com/jimmidyson/kcp-cluster-api/commit/b0fcd54ad755ae1ef0e64da5a755bf8f9626f815))
* decide how the fork model scales to many providers ([#78](https://github.com/jimmidyson/kcp-cluster-api/issues/78)) ([62fbf66](https://github.com/jimmidyson/kcp-cluster-api/commit/62fbf66a50104b2c62fc1ba15210c74fe373d43a))
* explain logical clusters and how to find their IDs ([#76](https://github.com/jimmidyson/kcp-cluster-api/issues/76)) ([70bc97b](https://github.com/jimmidyson/kcp-cluster-api/commit/70bc97be053d8e054d220f6f6b61b699d79206b7))
* give the site a search box, and fix its edit links ([#100](https://github.com/jimmidyson/kcp-cluster-api/issues/100)) ([af1ca1b](https://github.com/jimmidyson/kcp-cluster-api/commit/af1ca1b603da3738170c653746c089adc5608df3))
* move superseded constitution amendments to a history file ([#28](https://github.com/jimmidyson/kcp-cluster-api/issues/28)) ([6149099](https://github.com/jimmidyson/kcp-cluster-api/commit/614909906fff107980f9e81916b2cf8bee603bea))
* reconcile the workspace-scale spec with the route taken ([#62](https://github.com/jimmidyson/kcp-cluster-api/issues/62)) ([b8571b4](https://github.com/jimmidyson/kcp-cluster-api/commit/b8571b44ceb20d6d587a9276011df6af623c1df1))
* record the Nutanix fork's drift, and correct ADR-0004's internal claim ([#94](https://github.com/jimmidyson/kcp-cluster-api/issues/94)) ([51586cc](https://github.com/jimmidyson/kcp-cluster-api/commit/51586cce7ba47a2cb141784693f17b3d23043fbd))
* record the state the demo reached ([#60](https://github.com/jimmidyson/kcp-cluster-api/issues/60)) ([5905106](https://github.com/jimmidyson/kcp-cluster-api/commit/5905106fa6a2ea988b242c8429f835c3656ea7fb))
* record what the docker backend reaches, and the two things stopping it ([#69](https://github.com/jimmidyson/kcp-cluster-api/issues/69)) ([5eae0b5](https://github.com/jimmidyson/kcp-cluster-api/commit/5eae0b56aed056f442e29829ab87a1ee02f3233e))
* record what the fleet wiring costs, and what could not be measured ([#34](https://github.com/jimmidyson/kcp-cluster-api/issues/34)) ([457ab9e](https://github.com/jimmidyson/kcp-cluster-api/commit/457ab9e6ba7b4d819c0d0d0fb9bf6f11be97ebeb))
* redirect the site root straight to the user docs ([#73](https://github.com/jimmidyson/kcp-cluster-api/issues/73)) ([f11ce6c](https://github.com/jimmidyson/kcp-cluster-api/commit/f11ce6cf98c5beeec3cf9111a5c28d522da43153))
* refresh README for the current task surface and CI ([#24](https://github.com/jimmidyson/kcp-cluster-api/issues/24)) ([e555555](https://github.com/jimmidyson/kcp-cluster-api/commit/e55555502550d7c15c19ffd844ac70d4e2ac2706))
* retire ADR-0004's shared-plumbing layer, which its trigger disproved ([#81](https://github.com/jimmidyson/kcp-cluster-api/issues/81)) ([69f6430](https://github.com/jimmidyson/kcp-cluster-api/commit/69f64302661aede4a893b8056a2c4777b770dbd8))
* settle ADR-0004's L1 extraction claim by running it ([#79](https://github.com/jimmidyson/kcp-cluster-api/issues/79)) ([7c48244](https://github.com/jimmidyson/kcp-cluster-api/commit/7c482445dbb30e7a510bf8d1db228b52dd722c29))
* spell kubectl out in full in the walkthrough ([#74](https://github.com/jimmidyson/kcp-cluster-api/issues/74)) ([bc679aa](https://github.com/jimmidyson/kcp-cluster-api/commit/bc679aa16ec128772a35addc912e6ebdef28592e))
* tighten AGENTS.md and make commit authorship enforceable ([#27](https://github.com/jimmidyson/kcp-cluster-api/issues/27)) ([77656a5](https://github.com/jimmidyson/kcp-cluster-api/commit/77656a5136e43ed5aeb185ef37ccef8401ed7e78))
* update conversion plan with Phase 2 completion and Phase 3 status ([#29](https://github.com/jimmidyson/kcp-cluster-api/issues/29)) ([43302e1](https://github.com/jimmidyson/kcp-cluster-api/commit/43302e1a389a51915ed6f4a55dca5cd0fb4bff2e))


### Build and Dependencies

* bump amannn/action-semantic-pull-request from 5.5.3 to 6.1.1 in the all-github-actions group ([#23](https://github.com/jimmidyson/kcp-cluster-api/issues/23)) ([f5e378c](https://github.com/jimmidyson/kcp-cluster-api/commit/f5e378cdd7ce31bd194aaf3f65732b3a95570cf4))
* pin the fork at v1.15.0-kcp.11 ([#72](https://github.com/jimmidyson/kcp-cluster-api/issues/72)) ([52658ab](https://github.com/jimmidyson/kcp-cluster-api/commit/52658abd9dc3efc10cf2c05aa2d770ae548cbfbf))
* pin the fork at v1.15.0-kcp.7 ([#36](https://github.com/jimmidyson/kcp-cluster-api/issues/36)) ([8bf21ab](https://github.com/jimmidyson/kcp-cluster-api/commit/8bf21ab41601d2ac114e6dff7e9b6d3878f3f22d))
* pin the fork at v1.15.0-kcp.9 ([#59](https://github.com/jimmidyson/kcp-cluster-api/issues/59)) ([41e1ec2](https://github.com/jimmidyson/kcp-cluster-api/commit/41e1ec2ed9b7e7ecbbefa050a2f9c9a0bdee79ad))
