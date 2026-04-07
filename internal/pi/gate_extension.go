package pi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

const gateExtensionTitle = "acp-adapter permission gate"

const gateExtensionSource = `import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";

const gateTitle = "` + gateExtensionTitle + `";

function looksNetworkCommand(command: string): boolean {
	const text = command.toLowerCase();
	const needles = [
		"http://",
		"https://",
		"curl ",
		"wget ",
		"git clone ",
		"npm install ",
		"pnpm install ",
		"yarn add ",
		"go get ",
		"cargo add ",
	];
	return needles.some((needle) => text.includes(needle));
}

function payloadForTool(event: { toolCallId: string; toolName: string; input: Record<string, unknown> }) {
	if (event.toolName === "bash") {
		const command = String(event.input.command ?? "");
		return {
			gate: "acp-adapter",
			version: 1,
			toolCallId: event.toolCallId,
			toolName: event.toolName,
			approval: looksNetworkCommand(command) ? "network" : "command",
			command,
			message: command,
		};
	}

	if (event.toolName === "write") {
		const path = String(event.input.path ?? "");
		const content = String(event.input.content ?? "");
		return {
			gate: "acp-adapter",
			version: 1,
			toolCallId: event.toolCallId,
			toolName: event.toolName,
			approval: "file",
			files: path ? [path] : [],
			message: path ? "write " + path + " (" + content.length + " bytes)" : "write file",
		};
	}

	if (event.toolName === "edit") {
		const path = String(event.input.path ?? "");
		const edits = Array.isArray(event.input.edits) ? event.input.edits.length : 0;
		return {
			gate: "acp-adapter",
			version: 1,
			toolCallId: event.toolCallId,
			toolName: event.toolName,
			approval: "file",
			files: path ? [path] : [],
			message: path ? "edit " + path + " (" + edits + " replacements)" : "edit file",
		};
	}

	return undefined;
}

export default function (pi: ExtensionAPI) {
	pi.on("tool_call", async (event, ctx) => {
		if (event.toolName !== "bash" && event.toolName !== "write" && event.toolName !== "edit") {
			return undefined;
		}

		const payload = payloadForTool(event);
		if (!payload) {
			return undefined;
		}

		if (!ctx.hasUI) {
			return { block: true, reason: "Blocked by acp-adapter permission gate (no UI available)" };
		}

		const allowed = await ctx.ui.confirm(gateTitle, JSON.stringify(payload), { timeout: 30000 });
		if (!allowed) {
			return { block: true, reason: "Blocked by user" };
		}

		return undefined;
	});
}
`

func ensureGateExtensionFile() (string, error) {
	sum := sha256.Sum256([]byte(gateExtensionSource))
	filename := "acp-adapter-pi-gate-" + hex.EncodeToString(sum[:8]) + ".ts"
	path := filepath.Join(os.TempDir(), filename)

	current, err := os.ReadFile(path)
	if err == nil && string(current) == gateExtensionSource {
		return path, nil
	}
	if err := os.WriteFile(path, []byte(gateExtensionSource), 0o600); err != nil {
		return "", fmt.Errorf("write gate extension: %w", err)
	}
	return path, nil
}
