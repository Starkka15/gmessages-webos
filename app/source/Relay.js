/* Talking to the relay, which runs on this TouchPad and answers on a local address. */
var GM = GM || {};

GM.RELAY = "http://127.0.0.1:7340";

// Where the relay looks for a browser's sign-in, on the USB drive.
GM.COOKIE_FILE = "gmessages-cookies.txt";

GM.log = function(msg) {
	try { console.log("[gmessages] " + msg); } catch (e) {}
};

GM.Relay = {
	// Ask the relay. callback(error, answer): error is words for the person, or "".
	request: function(method, path, body, callback, wait) {
		var req = new XMLHttpRequest();
		var done = false;
		function finish(error, answer) {
			if (!done) {
				done = true;
				clearTimeout(timer);
				callback(error, answer || {});
			}
		}
		var timer = setTimeout(function() {
			try { req.abort(); } catch (e) {}
			finish("The relay did not answer in time.");
		}, wait || 15000);
		try {
			req.open(method, GM.RELAY + path, true);
			// The relay answers nothing but reads without this.
			req.setRequestHeader("X-GMRelay", "1");
			if (body) {
				req.setRequestHeader("Content-Type", "application/json");
			}
			req.onreadystatechange = function() {
				if (req.readyState !== 4) {
					return;
				}
				var answer = null;
				try {
					answer = enyo.json.parse(req.responseText);
				} catch (e) {}
				if (req.status === 0) {
					finish("offline");
				} else if (req.status >= 400 || !answer) {
					finish(answer && answer.error ? String(answer.error) : "The relay answered with an error (" + req.status + ").");
				} else {
					finish("", answer);
				}
			};
			req.send(body ? enyo.json.stringify(body) : null);
		} catch (e) {
			GM.log("request " + path + ": " + e);
			finish("offline");
		}
	},

	get: function(path, callback, wait) {
		this.request("GET", path, null, callback, wait);
	},

	post: function(path, body, callback, wait) {
		this.request("POST", path, body || {}, callback, wait);
	}
};
