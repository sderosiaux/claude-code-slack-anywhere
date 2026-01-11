.PHONY: build install clean restart reload

BINARY=claude-code-slack-anywhere
BUILD_TIME=$(shell date +%Y%m%d-%H%M%S)
LDFLAGS=-ldflags "-X main.buildTime=$(BUILD_TIME)"

build:
	go build $(LDFLAGS) -o $(BINARY)
	@if [ "$$(uname)" = "Darwin" ]; then \
		codesign -f -s - $(BINARY) 2>/dev/null || true; \
	fi

restart: build
	pkill -f $(BINARY) 2>/dev/null || true
	nohup ./$(BINARY) listen > server.log 2>&1 &
	@sleep 1
	@echo "Restarted (PID: $$(pgrep -f $(BINARY)))"

install: build
	mkdir -p ~/bin
	install -m 755 $(BINARY) ~/bin/$(BINARY)
	@echo "Installed to ~/bin/$(BINARY)"

reload: install
	launchctl unload ~/Library/LaunchAgents/com.ccsa.plist 2>/dev/null || true
	launchctl load ~/Library/LaunchAgents/com.ccsa.plist
	@sleep 2
	@echo "Reloaded launchd service"
	@tail -5 ~/.ccsa.log

clean:
	rm -f $(BINARY)
