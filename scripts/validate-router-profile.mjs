#!/usr/bin/env node
// Development-only validation against a trusted model-router source checkout.
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const args = process.argv.slice(2);
if (args.length !== 2) {
  console.error("Usage: node scripts/validate-router-profile.mjs ROUTER_SOURCE PROFILE_JSON");
  process.exitCode = 1;
} else {
  try {
    const { loadConfig } = await import(pathToFileURL(resolve(args[0], "lib/config.mjs")));
    const userFile = JSON.parse(await readFile(args[1], "utf8"));
    const config = loadConfig({ provider: "proxy", userFile });
    console.log("Valid proxy router profile: " + Object.keys(config.models).length + " models.");
  } catch {
    console.error(
      "Router profile validation failed. Check the trusted source checkout, profile path and schema.",
    );
    process.exitCode = 1;
  }
}
