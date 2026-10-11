// Checks the app's code against the rules it is written to (see AGENTS.md): only what
// the TouchPad's 2011 web engine and node 0.4 understand, Enyo 1 and not Enyo 2, the
// house style, license headers on files that came from LibreOffice, and no version bump.
//   node tools/check.js
// Prints each problem as file:line and exits 1 if there are any. Run it before every commit.
// It reads the code as text, so it can be fooled; a clean run is necessary, not sufficient.
var fs = require("fs");
var path = require("path");
var cp = require("child_process");
var root = path.join(__dirname, "..");
var problems = 0;

function report(file, line, message) {
	console.log(path.relative(root, file).split(path.sep).join("/") + ":" + line + "  " + message);
	problems++;
}

// The code with comments, strings and regular expressions blanked out (line breaks kept),
// so a rule about code is not tripped by words in a comment or a string.
function codeOnly(text) {
	var out = "";
	var i = 0;
	var n = text.length;
	var last = "";  // the last character of code that was not a space
	function blank(from, to) {
		for (var k = from; k < to; k++) {
			out += text.charAt(k) === "\n" ? "\n" : " ";
		}
	}
	while (i < n) {
		var c = text.charAt(i);
		var d = text.charAt(i + 1);
		var end;
		if (c === "/" && d === "/") {
			end = text.indexOf("\n", i);
			end = end < 0 ? n : end;
			blank(i, end);
			i = end;
		} else if (c === "/" && d === "*") {
			end = text.indexOf("*/", i + 2);
			end = end < 0 ? n : end + 2;
			blank(i, end);
			i = end;
		} else if (c === "\"" || c === "'") {
			end = i + 1;
			while (end < n && text.charAt(end) !== c && text.charAt(end) !== "\n") {
				end += text.charAt(end) === "\\" ? 2 : 1;
			}
			out += c;
			blank(i + 1, Math.min(end, n));
			out += c;
			i = end + 1;
			last = c;
		} else if (c === "/" && (last === "" || /[(,=:\[!&|?{};+\-*%<>~^]/.test(last) || /\breturn\s*$/.test(out.slice(-9)))) {
			// A regular expression: a slash where a value is expected.
			end = i + 1;
			var inClass = false;
			while (end < n && text.charAt(end) !== "\n" && (inClass || text.charAt(end) !== "/")) {
				if (text.charAt(end) === "\\") { end++; }
				else if (text.charAt(end) === "[") { inClass = true; }
				else if (text.charAt(end) === "]") { inClass = false; }
				end++;
			}
			out += "/";
			blank(i + 1, end);
			out += "/";
			i = end + 1;
			last = "/";
		} else {
			out += c;
			if (!/\s/.test(c)) {
				last = c;
			}
			i++;
		}
	}
	return out;
}

// What the 2011 engines do not have. [pattern, what to say]
var ES5 = [
	[/\b(let|const)\s+[\w{\[]/, "let/const: use var (ES5 only)"],
	[/=>/, "arrow function: use function (ES5 only)"],
	[/`/, "template string: use + (ES5 only)"],
	[/\bclass\s+\w+/, "class: use enyo.kind or a plain object (ES5 only)"],
	[/\basync\s+function|\bawait\s/, "async/await: use callbacks (ES5 only)"],
	[/\.\.\.[\w\[]/, "spread/rest: not in ES5"],
	[/\bfor\s*\([^)]*\sof\s/, "for...of: use an index loop (ES5 only)"],
	[/\bnew\s+(Promise|Map|Set|WeakMap|Symbol|Proxy)\b|\bPromise\.\w+\(/, "Promise/Map/Set: not on the TouchPad"],
	[/\.(includes|startsWith|endsWith|padStart|padEnd|repeat|findIndex|flat|flatMap)\(/, "a string/array method newer than ES5"],
	[/\bObject\.(assign|entries|values)\(|\bArray\.(from|of)\(|\bNumber\.(isInteger|isFinite|isNaN)\(/, "a built-in newer than ES5"],
	[/\bfetch\(/, "fetch: use XMLHttpRequest (see an XMLHttpRequest helper)"],
	[/(?<!\benyo)\.bind\(/,"Function.bind is missing on this engine: use enyo.bind(this, ...) or a closure"]
];
// Enyo 2 (and Onyx) spellings that do nothing, or the wrong thing, in Enyo 1.
var ENYO1 = [
	[/\bontap\s*:/, "ontap is Enyo 2: Enyo 1 uses onclick"],
	[/\bclasses\s*:/, "classes is Enyo 2: Enyo 1 uses className"],
	[/\bonyx\./, "Onyx belongs to Enyo 2"],
	[/Fittable(Rows|Columns)/, "Fittable layouts are Enyo 2: use VFlexBox/HFlexBox with flex"],
	[/\btag\s*:\s*["']/, "tag is Enyo 2: Enyo 1 uses nodeTag"],
	[/\benyo\.(ready|Signals|Collection|Model|bindable)\b/, "an Enyo 2 API"],
	[/\b(addRemoveClass|applyStyle)\s*\(.*\)\s*;?\s*\/\/\s*enyo2/, "an Enyo 2 idiom"],
	[/\brequire\s*\(/, "require() in the app: the app is loaded by enyo.depends, not modules"]
];
var STYLE = [
	[/[^=!<>]==[^=]|!=[^=]/, "use === and !=="],
	[/\bconsole\.(log|warn|error)\(/, "use GM.log, which the tools read back from the device"]
];

function checkFile(file, rules, options) {
	var text = fs.readFileSync(file, "utf8").split("\r\n").join("\n");
	if (/derived from the LibreOffice project/.test(text.substring(0, 600))) {
		if (!/Mozilla Public\s+\*?\s*License, v\. 2\.0/.test(text.substring(0, 900).replace(/\n \*/g, " "))) {
			report(file, 1, "a file from LibreOffice must keep the MPL 2.0 notice at its top");
		}
	}
	if (/^\/\/ GENERATED/m.test(text.substring(0, 2500))) {
		return;  // made by a generator: fix the generator, not the file
	}
	var code = codeOnly(text).split("\n");
	var lines = text.split("\n");
	for (var i = 0; i < code.length; i++) {
		for (var r = 0; r < rules.length; r++) {
			if (rules[r][0].test(code[i])) {
				// GM.log itself is where console.log lives.
				if (/console/.test(rules[r][0].source) &&/PP\.log = |try \{ console\.log/.test(lines[i] + (lines[i - 1] || ""))) {
					continue;
				}
				report(file, i + 1, rules[r][1]);
			}
		}
		if (options.tabs && /^ +\S/.test(lines[i]) && !/^ \*/.test(lines[i])) {
			report(file, i + 1, "indent with tabs");
		}
	}
}

var appDir = path.join(root, "app", "source");
var sources = fs.readdirSync(appDir).filter(function(f) { return /\.js$/.test(f); });
sources.forEach(function(f) {
	checkFile(path.join(appDir, f), ES5.concat(ENYO1, STYLE), {tabs: true});
});
(fs.existsSync(path.join(root, "service")) ? fs.readdirSync(path.join(root, "service")) : []).filter(function(f) { return /\.js$/.test(f); }).forEach(function(f) {
	// The service runs on node 0.4: ES5 as well, and console.log is its log.
	checkFile(path.join(root, "service", f), ES5.concat([STYLE[0]]), {tabs: true});
});

// Every source file is loaded, and nothing is loaded that is not there.
var depends = fs.readFileSync(path.join(root, "app", "depends.js"), "utf8");
sources.forEach(function(f) {
	if (depends.indexOf("\"source/" + f + "\"") < 0) {
		report(path.join(root, "app", "depends.js"), 1, "source/" + f + " is not listed, so the app will not load it");
	}
});
(depends.match(/"source\/[^"]+"/g) || []).forEach(function(entry) {
	if (!fs.existsSync(path.join(root, "app", entry.replace(/"/g, "")))) {
		report(path.join(root, "app", "depends.js"), 1, entry + " is listed but does not exist");
	}
});

// The version changes only when a release is cut, and the person decides that.
["app/appinfo.json", "package/packageinfo.json", "service/services.json"].forEach(function(rel) {
	try {
		var was = JSON.parse(cp.execFileSync("git", ["show", "HEAD:" + rel], {cwd: root, stdio: ["ignore", "pipe", "ignore"]}).toString());
		var now = JSON.parse(fs.readFileSync(path.join(root, rel), "utf8"));
		if (was.version !== now.version) {
			report(path.join(root, rel), 1, "version changed from " + was.version + " to " + now.version + ": only when the person asks for a release");
		}
	} catch (e) {}
});

console.log(problems ? problems + " problem(s)" : "check passed: " + sources.length + " app sources");
process.exit(problems ? 1 : 0);
