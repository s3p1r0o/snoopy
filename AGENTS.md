# Agent instructions

## dsh plugin bundles

When you create a dsh plugin or bundle in this repository (dsh's Creator mode
writes them in the workspace), put each one in its own directory under
`bundles/<name>/`, for example `bundles/my-plugin/`. That directory is
git-ignored: those bundles are local experiments, not part of snoopy. Install
them with `plugin_manager` (`install_bundle`, target the absolute directory).

Do not write plugin code anywhere else in the repository, and do not change
`profile/*.patch.yml`: those are snoopy's own routing patches.
