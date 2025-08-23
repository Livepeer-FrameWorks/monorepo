// @ts-check

/**
 * Format bytes to human readable format
 * @param {number} bytes
 * @param {number} decimals
 * @returns {string}
 */
export function formatBytes(bytes, decimals = 2) {
  if (!bytes || bytes === 0) return '0 Bytes';

  const k = 1024;
  const dm = decimals < 0 ? 0 : decimals;
  const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB', 'PB'];

  const i = Math.floor(Math.log(bytes) / Math.log(k));

  return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i];
}

/**
 * Format duration in seconds to human readable format
 * @param {number} seconds
 * @returns {string}
 */
export function formatDuration(seconds) {
  if (!seconds || seconds === 0) return '0s';

  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainingSeconds = seconds % 60;

  if (hours > 0) {
    return `${hours}h ${minutes}m ${remainingSeconds}s`;
  }
  
  if (minutes > 0) {
    return `${minutes}m ${remainingSeconds}s`;
  }
  
  return `${remainingSeconds}s`;
}

/**
 * Format date to human readable format
 * @param {string | Date} date
 * @returns {string}
 */
export function formatDate(date) {
  if (!date) return 'N/A';

  const dateObj = typeof date === 'string' ? new Date(date) : date;
  
  if (isNaN(dateObj.getTime())) return 'Invalid Date';

  const now = new Date();
  const diffMs = now.getTime() - dateObj.getTime();
  const diffMins = Math.floor(diffMs / 60000);
  const diffHours = Math.floor(diffMs / 3600000);
  const diffDays = Math.floor(diffMs / 86400000);

  // Less than a minute ago
  if (diffMins < 1) {
    return 'Just now';
  }
  
  // Less than an hour ago
  if (diffMins < 60) {
    return `${diffMins}m ago`;
  }
  
  // Less than 24 hours ago
  if (diffHours < 24) {
    return `${diffHours}h ago`;
  }
  
  // Less than 7 days ago
  if (diffDays < 7) {
    return `${diffDays}d ago`;
  }
  
  // More than a week ago - show actual date
  return dateObj.toLocaleDateString('en-US', {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit'
  });
}

/**
 * Format timestamp to readable date and time
 * @param {string | Date} timestamp
 * @returns {string}
 */
export function formatTimestamp(timestamp) {
  if (!timestamp) return 'N/A';

  const dateObj = typeof timestamp === 'string' ? new Date(timestamp) : timestamp;
  
  if (isNaN(dateObj.getTime())) return 'Invalid Date';

  return dateObj.toLocaleDateString('en-US', {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  });
}

/**
 * Format number with thousands separator
 * @param {number} num
 * @returns {string}
 */
export function formatNumber(num) {
  if (num === null || num === undefined || isNaN(num)) return 'N/A';
  
  return new Intl.NumberFormat('en-US').format(num);
}

/**
 * Format percentage
 * @param {number} value
 * @param {number} total
 * @param {number} decimals
 * @returns {string}
 */
export function formatPercentage(value, total, decimals = 1) {
  if (!value || !total || total === 0) return '0%';
  
  const percentage = (value / total) * 100;
  return percentage.toFixed(decimals) + '%';
}

/**
 * Format bitrate in kbps to human readable format
 * @param {number} kbps
 * @returns {string}
 */
export function formatBitrate(kbps) {
  if (!kbps || kbps === 0) return '0 kbps';

  if (kbps >= 1000) {
    return (kbps / 1000).toFixed(1) + ' Mbps';
  }
  
  return kbps + ' kbps';
}

/**
 * Format resolution string
 * @param {string} resolution
 * @returns {string}
 */
export function formatResolution(resolution) {
  if (!resolution) return 'N/A';
  
  // Common resolution mappings
  const resolutionMap = {
    '1920x1080': '1080p',
    '1280x720': '720p',
    '854x480': '480p',
    '640x360': '360p',
    '426x240': '240p',
    '3840x2160': '4K',
    '2560x1440': '1440p'
  };
  
  return resolutionMap[resolution] || resolution;
}

/**
 * Format currency amount
 * @param {number} amount
 * @param {string} currency
 * @returns {string}
 */
export function formatCurrency(amount, currency = 'USD') {
  if (amount === null || amount === undefined || isNaN(amount)) return 'N/A';
  
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: currency
  }).format(amount);
}

/**
 * Format uptime in milliseconds to human readable format
 * @param {number} uptimeMs
 * @returns {string}
 */
export function formatUptime(uptimeMs) {
  if (!uptimeMs || uptimeMs === 0) return '0s';

  const seconds = Math.floor(uptimeMs / 1000);
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainingSeconds = seconds % 60;

  const parts = [];
  if (days > 0) parts.push(`${days}d`);
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0) parts.push(`${minutes}m`);
  if (remainingSeconds > 0) parts.push(`${remainingSeconds}s`);

  return parts.join(' ') || '0s';
}