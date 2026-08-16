package com.callscreen.feature.business

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

import androidx.compose.material.icons.automirrored.filled.ArrowBack

@OptIn(ExperimentalMaterial3Api::class)
@Composable
public fun BusinessDashboardScreen(
    onBackClick: () -> Unit = {},
    onConfigureClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    val navyBlue = Color(0xFF1E3A8A)
    val cardBg = MaterialTheme.colorScheme.surface

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Analytics Dashboard", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold) },
                navigationIcon = {
                    IconButton(onClick = onBackClick) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },

                actions = {
                    IconButton(onClick = onConfigureClick) {
                        Icon(Icons.Default.Settings, contentDescription = "Settings")
                    }
                }
            )
        }
    ) { padding ->
        LazyColumn(
            modifier = modifier
                .fillMaxSize()
                .padding(padding)
                .background(MaterialTheme.colorScheme.background)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            // Organization Health Header Card
            item {
                Card(
                    shape = RoundedCornerShape(20.dp),
                    colors = CardDefaults.cardColors(containerColor = navyBlue),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Row(
                        modifier = Modifier.padding(20.dp).fillMaxWidth(),
                        horizontalArrangement = Arrangement.SpaceBetween,
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        Column {
                            Text("Organization Health", fontSize = 13.sp, color = Color.White.copy(alpha = 0.8f))
                            Text("98%", fontWeight = FontWeight.ExtraBold, fontSize = 32.sp, color = Color.White)
                            Text("Excellent protection across all endpoints", fontSize = 11.sp, color = Color.White.copy(alpha = 0.8f))
                        }
                        Surface(shape = CircleShape, color = Color.White.copy(alpha = 0.15f)) {
                            Icon(Icons.Default.Shield, contentDescription = null, tint = Color.White, modifier = Modifier.padding(12.dp).size(32.dp))
                        }
                    }
                }
            }

            // Key Metrics Row (2x2 Grid)
            item {
                Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        MetricSummaryCard("Total Calls Handled", "12,450", "+14% vs last month", Modifier.weight(1f))
                        MetricSummaryCard("Avg Handling Time", "1m 12s", "-5s vs last month", Modifier.weight(1f))
                    }
                    Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        MetricSummaryCard("Resolution Rate", "94.2%", "+2.1% vs last month", Modifier.weight(1f))
                        MetricSummaryCard("Fraud Prevented", "3,204", "Threats neutralized", Modifier.weight(1f))
                    }
                }
            }

            // Live Active Conversations & Call Queue
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = cardBg),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
                            Text("Live Active Conversations", fontWeight = FontWeight.Bold, fontSize = 15.sp)
                            Surface(shape = RoundedCornerShape(12.dp), color = Color(0xFFDCFCE7)) {
                                Text("3 Active", modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp), fontSize = 11.sp, fontWeight = FontWeight.Bold, color = Color(0xFF166534))
                            }
                        }

                        ActiveCallRow("+1 (555) 0192", "Sales Inquiry", "😊")
                        ActiveCallRow("Unknown Caller", "Screening...", "😐")
                    }
                }
            }

            // Trending Issues Section
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = cardBg),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        Text("Trending Issues", fontWeight = FontWeight.Bold, fontSize = 15.sp)

                        TrendingIssueItem("Billing Questions", "Resolved automatically 98% of time", "3,420", "High", Color(0xFFF1F5F9))
                        TrendingIssueItem("Feature Requests", "Routed to product feedback queue", "1,205", "Medium", Color(0xFFF1F5F9))
                        TrendingIssueItem("Suspected Spam/Fraud", "Blocked before reaching user", "890", "Spike", Color(0xFFFEE2E2))
                    }
                }
            }
        }
    }
}

@Composable
private fun MetricSummaryCard(title: String, value: String, subtitle: String, modifier: Modifier = Modifier) {
    Card(
        shape = RoundedCornerShape(16.dp),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface),
        modifier = modifier
    ) {
        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
            Text(title, fontSize = 11.sp, color = Color.Gray)
            Text(value, fontWeight = FontWeight.ExtraBold, fontSize = 22.sp, color = MaterialTheme.colorScheme.onSurface)
            Text(subtitle, fontSize = 10.sp, color = Color(0xFF16A34A), fontWeight = FontWeight.SemiBold)
        }
    }
}

@Composable
private fun ActiveCallRow(number: String, intent: String, emoji: String) {
    Row(
        modifier = Modifier.fillMaxWidth().clip(RoundedCornerShape(12.dp)).background(Color(0xFFF8FAFC)).padding(12.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically
    ) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Icon(Icons.Default.PhoneInTalk, contentDescription = null, tint = MaterialTheme.colorScheme.primary)
            Column {
                Text(number, fontWeight = FontWeight.Bold, fontSize = 13.sp)
                Text(intent, fontSize = 11.sp, color = Color.Gray)
            }
        }
        Text(emoji, fontSize = 18.sp)
    }
}

@Composable
private fun TrendingIssueItem(title: String, desc: String, count: String, tag: String, tagBg: Color) {
    Row(
        modifier = Modifier.fillMaxWidth().clip(RoundedCornerShape(12.dp)).background(Color(0xFFF8FAFC)).padding(12.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically
    ) {
        Column(modifier = Modifier.weight(1f)) {
            Text(title, fontWeight = FontWeight.Bold, fontSize = 13.sp)
            Text(desc, fontSize = 11.sp, color = Color.Gray)
        }
        Column(horizontalAlignment = Alignment.End) {
            Text(count, fontWeight = FontWeight.Bold, fontSize = 12.sp)
            Surface(shape = RoundedCornerShape(4.dp), color = tagBg) {
                Text(tag, modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp), fontSize = 10.sp, fontWeight = FontWeight.Bold)
            }
        }
    }
}
