// helpers/datetime.js
export function formatRFC3339ToDate(rfc3339Timestamp) {
  if (!rfc3339Timestamp) return "";
  
  try {
    const date = new Date(rfc3339Timestamp);
    return date.toISOString().split('T')[0]; // Returns YYYY-MM-DD
  } catch (e) {
    console.error("Error parsing RFC3339 timestamp:", rfc3339Timestamp, e);
    return "";
  }
}

export function formatRFC3339ToTime(rfc3339Timestamp) {
  if (!rfc3339Timestamp) return "";
  
  try {
    const date = new Date(rfc3339Timestamp);
    return date.toTimeString().substring(0, 5); // Returns HH:MM
  } catch (e) {
    console.error("Error parsing RFC3339 timestamp:", rfc3339Timestamp, e);
    return "";
  }
}

export function formatRFC3339ToDatetime(rfc3339Timestamp) {
  if (!rfc3339Timestamp) return { date: "", time: "" };
  
  try {
    const date = new Date(rfc3339Timestamp);
    return {
      date: date.toISOString().split('T')[0], // YYYY-MM-DD
      time: date.toTimeString().substring(0, 5) // HH:MM
    };
  } catch (e) {
    console.error("Error parsing RFC3339 timestamp:", rfc3339Timestamp, e);
    return { date: "", time: "" };
  }
}

export function formatDateTimeToRFC3339(dateStr, timeStr) {
  if (!dateStr) return "";
  
  try {
    // If no time provided, use 00:00
    const time = timeStr || "00:00";
    const datetime = new Date(`${dateStr}T${time}:00`);
    const result = datetime.toISOString();
    
    // Debug logging
    console.log("formatDateTimeToRFC3339 input:", { dateStr, timeStr });
    console.log("formatDateTimeToRFC3339 output:", result, typeof result);
    
    return result;
  } catch (e) {
    console.error("Error creating RFC3339 timestamp:", dateStr, timeStr, e);
    return "";
  }
}

export function formatDateToRFC3339(dateStr) {
  return formatDateTimeToRFC3339(dateStr, "00:00");
}