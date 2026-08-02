/**
 * DataService
 * Handles all API requests to the server
 */
import { API } from "./constants.js";

export class DataService {
  constructor() {
    this.baseUrl = window.location.origin;

    this.fetchOptions = {
      method: "GET",
      mode: "cors",
      cache: "no-cache",
      credentials: "same-origin",
      redirect: "error",
      referrerPolicy: "no-referrer",
    };
  }

  /**
   * Get API URL with endpoint
   * @param {string} endpoint - API endpoint
   * @returns {string} Full API URL
   */
  _getUrl(endpoint) {
    return `${this.baseUrl}/${endpoint}`;
  }

  /**
   * Fetch data from API and parse as JSON
   * @param {string} endpoint - API endpoint
   * @returns {Promise<Object>} Parsed JSON response
   */
  async _fetchData(endpoint) {
    try {
      const response = await fetch(this._getUrl(endpoint), this.fetchOptions);
      return await response.json();
    } catch (error) {
      console.error(`Error fetching ${endpoint}:`, error);
      return null;
    }
  }

  /**
   * Check if logs are enabled
   * @returns {Promise<Object>} Log enabled status
   */
  async checkLogsEnabled() {
    return await this._fetchData(API.LOGS_ENABLED);
  }

  /**
   * Fetch application state
   * @returns {Promise<Object>} Application state data
   */
  async fetchState() {
    return await this._fetchData(API.STATE);
  }

  /**
   * Fetch logs
   * @returns {Promise<Array>} Log entries
   */
  async fetchLogs() {
    return await this._fetchData(API.LOGS);
  }

  /**
   * Fetch alert history
   * @returns {Promise<Array>} Alert history entries
   */
  async fetchAlertHistory() {
    return await this._fetchData(API.ALERT_HISTORY);
  }

  /**
   * Load initial state, logs, and alert history
   * @returns {Promise<Object>} Combined state data
   */
  async loadState() {
    try {
      // Check if logs are enabled
      const logsStatus = await this.checkLogsEnabled();
      if (logsStatus && logsStatus.enabled === false) {
        document.getElementById("logContainerWrapper").hidden = true;
        document.getElementById("alertHistoryContainerWrapper").hidden = true;
      }

      // Load state data
      const state = await this.fetchState();

      // Load logs and alert history if the panels are visible
      if (!document.getElementById("logContainerWrapper").hidden) {
        const logs = await this.fetchLogs();
        const alertHistory = await this.fetchAlertHistory();
        return { ...state, logs, alertHistory };
      }

      return state;
    } catch (error) {
      console.error("Error loading initial state:", error);
      return null;
    }
  }
}

