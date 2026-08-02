/**
 * AlertHistoryManager
 * Handles alert history entries display and management
 */
import { MAX_ALERT_HISTORY_ENTRIES } from './constants.js';

export class AlertHistoryManager {
  constructor(maxEntries = MAX_ALERT_HISTORY_ENTRIES) {
    this.entries = [];
    this.maxEntries = maxEntries;
    this.el = document.getElementById('alertHistory');
  }

  /**
   * Format timestamp to locale string
   * @param {number} timestamp - Unix timestamp in seconds
   * @returns {string} Formatted timestamp
   * @private
   */
  _formatTimestamp(timestamp) {
    return new Date(timestamp * 1000).toLocaleString();
  }

  /**
   * Format an alert history entry as a single display line
   * @param {Object} entry - Alert history entry
   * @returns {string} Formatted line
   * @private
   */
  _formatEntry(entry) {
    const icon = entry.resolved ? '💜' : '🚨';
    return `${icon} ${this._formatTimestamp(entry.time)} [${entry.severity}] ${entry.chain}: ${entry.message}`;
  }

  /**
   * Add an alert history entry to the display
   * @param {Object} entry - Alert history entry ({time, chain, chain_id, message, severity, resolved})
   */
  addEntry(entry) {
    if (this.entries.length >= this.maxEntries) {
      this.entries.pop();
    }

    this.entries.unshift(entry);

    this._updateDisplay();
  }

  /**
   * Load initial alert history entries
   * @param {Array} entries - Array of alert history entry objects, newest first
   */
  loadInitialEntries(entries) {
    if (!Array.isArray(entries)) return;

    // Process entries in reverse order (oldest first) so the final display, after
    // repeated unshift-based addEntry calls, ends up newest-first again.
    for (let i = entries.length - 1; i >= 0; i--) {
      this.addEntry(entries[i]);
    }
  }

  /**
   * Update the alert history display element with current entries
   * @private
   */
  _updateDisplay() {
    if (document.visibilityState !== 'hidden' && this.el) {
      this.el.textContent = this.entries.map((e) => this._formatEntry(e)).join('\n');
    }
  }

  /**
   * Clear all alert history entries
   */
  clear() {
    this.entries = [];
    this._updateDisplay();
  }
}
