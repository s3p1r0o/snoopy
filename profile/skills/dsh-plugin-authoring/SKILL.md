---
name: dsh-plugin-authoring
description: Where and how to create your own dsh plugin or bundle. Use it whenever you are asked to write, scaffold or install a new plugin, bundle or extension for yourself.
---
# Authoring your own dsh plugin

Create every plugin you write for yourself in its own directory under the
plugins directory of your dsh home:

    "$SNOOPY_PLUGINS_DIR/<package-name>/"

Run `echo "$SNOOPY_PLUGINS_DIR"` to get its absolute path. Never create a
plugin inside the workspace or the current repository: those files are the
user's project, and the plugin would end up committed with it.

1. Make the package a dsh bundle: a `package.json` with `"type": "module"`, a
   `name`, a `version`, and `"dsh": { "bundle": { "patch": "./cordis.patch.yml" } }`;
   `cordis.patch.yml` is a top-level YAML array of loader patch entries that
   `insert`s your plugin rows (`- insert: [{ id: <id>, name: <package-name> }]`).
   An installed bundle such as `@deepseek-ai/dsh-experimental-schedule-bundle`
   shows the shape.
2. Install it with the `plugin_manager` tool: action `install_bundle`, target
   the absolute path of the plugin directory. This asks for approval.
3. The change applies on the next launch (the Web app reloads it live).
